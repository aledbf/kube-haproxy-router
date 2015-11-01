/*
Copyright 2015 The Kubernetes Authors All rights reserved.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/golang/glog"
	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/controller/framework"
	"k8s.io/kubernetes/pkg/fields"
	"k8s.io/kubernetes/pkg/util"

	"k8s.io/kubernetes/pkg/client/cache"
	"k8s.io/kubernetes/pkg/util/workqueue"

	"github.com/aledbf/kube-haproxy-router/haproxy"
)

const (
	reloadQPS                = 10.0
	resyncPeriod             = 10 * time.Second
	lbApiPort                = 8081
	lbAlgorithmKey           = "serviceloadbalancer/lb.algorithm"
	lbHostKey                = "serviceloadbalancer/lb.host"
	lbCookieStickySessionKey = "serviceloadbalancer/lb.cookie-sticky-session"
)

var (
	// See https://cbonte.github.io/haproxy-dconv/configuration-1.5.html#4.2-balance
	// In brief:
	//  * roundrobin: backend with the highest weight (how is this set?) receives new connection
	//  * leastconn: backend with least connections receives new connection
	//  * first: first server sorted by server id, with an available slot receives connection
	//  * source: connection given to backend based on hash of source ip
	supportedAlgorithms = []string{"roundrobin", "leastconn", "first", "source"}
)

type serviceAnnotations map[string]string

func (s serviceAnnotations) getAlgorithm() (string, bool) {
	val, ok := s[lbAlgorithmKey]
	return val, ok
}

func (s serviceAnnotations) getHost() (string, bool) {
	val, ok := s[lbHostKey]
	return val, ok
}

func (s serviceAnnotations) getCookieStickySession() (string, bool) {
	val, ok := s[lbCookieStickySessionKey]
	return val, ok
}

// LBRule encapsulates the definition of the name and the list of URLs
// of the a service to be exposed by the load balancer
type LBRule struct {
	ACL  string
	URLs []string
}

type LoadBalancerController interface {
	getRules(s *api.Service, servicePort int) *LBRule
}

// LoadBalancerController watches the kubernetes api and adds/removes services
// from the loadbalancer, via loadBalancerConfig.
type HaproxyController struct {
	LoadBalancerController
	cfg               *haproxy.LoadBalancer
	queue             *workqueue.Type
	client            *unversioned.Client
	epController      *framework.Controller
	svcController     *framework.Controller
	svcLister         cache.StoreToServiceLister
	epLister          cache.StoreToEndpointsLister
	reloadRateLimiter util.RateLimiter
	template          string
	targetService     string
	forwardServices   bool
	tcpServices       map[string]int
	httpPort          int
}

// getServices returns a list of services and their endpoints.
func (lbc *HaproxyController) getServices() (httpSvc []service, tcpSvc []service) {
	ep := []string{}
	services, _ := lbc.svcLister.List()
	for _, s := range services.Items {
		if s.Spec.Type == api.ServiceTypeLoadBalancer {
			glog.Infof("Ignoring service %v, it already has a loadbalancer", s.Name)
			continue
		}
		for _, servicePort := range s.Spec.Ports {
			// TODO: headless services?
			sName := s.Name
			if servicePort.Protocol == api.ProtocolUDP ||
				(lbc.targetService != "" && lbc.targetService != sName) {
				glog.Infof("Ignoring %v: %+v", sName, servicePort)
				continue
			}

			ep = lbc.getEndpoints(&s, &servicePort)
			if len(ep) == 0 {
				glog.Infof("No endpoints found for service %v, port %+v",
					sName, servicePort)
				continue
			}

			rules := lbc.getRules(&s, servicePort.Port)
			if len(rules.URLs) == 0 {
				glog.Infof("No rules found for service %v, port %+v", sName, servicePort)
				continue
			}

			newSvc := service{
				Name: rules.ACL,
				Ep:   ep,
				URLs: rules.URLs,
			}

			if val, ok := serviceAnnotations(s.ObjectMeta.Annotations).getHost(); ok {
				newSvc.Host = val
			}

			if val, ok := serviceAnnotations(s.ObjectMeta.Annotations).getAlgorithm(); ok {
				for _, current := range supportedAlgorithms {
					if val == current {
						newSvc.Algorithm = val
						break
					}
				}
			} else {
				newSvc.Algorithm = lbc.cfg.Algorithm
			}

			// By default sticky session is disabled
			newSvc.SessionAffinity = false
			if s.Spec.SessionAffinity != "" {
				newSvc.SessionAffinity = true
			}

			if port, ok := lbc.tcpServices[sName]; ok && port == servicePort.Port {
				newSvc.FrontendPort = servicePort.Port
				tcpSvc = append(tcpSvc, newSvc)
			} else {
				if val, ok := serviceAnnotations(s.ObjectMeta.Annotations).getCookieStickySession(); ok {
					b, err := strconv.ParseBool(val)
					if err == nil {
						newSvc.CookieStickySession = b
					}
				}

				newSvc.FrontendPort = lbc.httpPort
				httpSvc = append(httpSvc, newSvc)
			}
			glog.Infof("Found service: %+v", newSvc)
		}
	}

	sort.Sort(serviceByName(httpSvc))
	sort.Sort(serviceByName(tcpSvc))

	return
}

// sync all services with the loadbalancer.
func (lbc *HaproxyController) sync(dryRun bool) error {
	if !lbc.epController.HasSynced() || !lbc.svcController.HasSynced() {
		time.Sleep(100 * time.Millisecond)
		return errDeferredSync
	}

	httpSvc, tcpSvc := lbc.getServices()
	if len(httpSvc) == 0 && len(tcpSvc) == 0 {
		return nil
	}

	if err := lbc.cfg.write(
		map[string][]service{
			"http": httpSvc,
			"tcp":  tcpSvc,
		}, dryRun); err != nil {
		return err
	}

	if dryRun {
		return nil
	}

	lbc.reloadRateLimiter.Accept()
	return lbc.cfg.reload()
}

// worker handles the work queue.
func (lbc *HaproxyController) worker() {
	for {
		key, _ := lbc.queue.Get()
		glog.Infof("Sync triggered by service %v", key)
		if err := lbc.sync(false); err != nil {
			glog.Infof("Requeuing %v because of error: %v", key, err)
			lbc.queue.Add(key)
		}
		lbc.queue.Done(key)
	}
}

// newLoadBalancerController creates a new controller from the given config.
func newLoadBalancerController(cfg *LoadBalancerConfig, kubeClient *unversioned.Client, namespace string) *HaproxyController {
	lbc := HaproxyController{
		cfg:    cfg,
		client: kubeClient,
		queue:  workqueue.New(),
		reloadRateLimiter: util.NewTokenBucketRateLimiter(
			reloadQPS, int(reloadQPS)),
		targetService: *targetService,
		httpPort:      *httpPort,
		tcpServices:   map[string]int{},
	}

	for _, service := range strings.Split(*tcpServices, ",") {
		portSplit := strings.Split(service, ":")
		if len(portSplit) != 2 {
			glog.Errorf("Ignoring misconfigured TCP service %v", service)
			continue
		}
		if port, err := strconv.Atoi(portSplit[1]); err != nil {
			glog.Errorf("Ignoring misconfigured TCP service %v: %v", service, err)
			continue
		} else {
			lbc.tcpServices[portSplit[0]] = port
		}
	}

	enqueue := func(obj interface{}) {
		key, err := keyFunc(obj)
		if err != nil {
			glog.Infof("Couldn't get key for object %+v: %v", obj, err)
			return
		}
		lbc.queue.Add(key)
	}

	eventHandlers := framework.ResourceEventHandlerFuncs{
		AddFunc:    enqueue,
		DeleteFunc: enqueue,
		UpdateFunc: func(old, cur interface{}) {
			if !reflect.DeepEqual(old, cur) {
				enqueue(cur)
			}
		},
	}

	lbc.svcLister.Store, lbc.svcController = framework.NewInformer(
		cache.NewListWatchFromClient(
			lbc.client, "services", namespace, fields.Everything()),
		&api.Service{}, resyncPeriod, eventHandlers)

	lbc.epLister.Store, lbc.epController = framework.NewInformer(
		cache.NewListWatchFromClient(
			lbc.client, "endpoints", namespace, fields.Everything()),
		&api.Endpoints{}, resyncPeriod, eventHandlers)

	return &lbc
}

func (lbc *HaproxyController) dryRun() {
	var err error
	for err = lbc.sync(true); err == errDeferredSync; err = lbc.sync(true) {
	}
	if err != nil {
		glog.Infof("ERROR: %+v", err)
	}
}

// getEndpoints returns a list of <endpoint ip>:<port> for a given service/target port combination.
func (lbc *HaproxyController) getEndpoints(
	s *api.Service, servicePort *api.ServicePort) (endpoints []string) {
	ep, err := lbc.epLister.GetServiceEndpoints(s)
	if err != nil {
		return
	}

	// The intent here is to create a union of all subsets that match a targetPort.
	// We know the endpoint already matches the service, so all pod ips that have
	// the target port are capable of service traffic for it.
	for _, ss := range ep.Subsets {
		for _, epPort := range ss.Ports {
			if epPort.Protocol == api.ProtocolUDP {
				continue
			}
			var targetPort int
			switch servicePort.TargetPort.Kind {
			case util.IntstrInt:
				if epPort.Port == servicePort.TargetPort.IntVal {
					targetPort = epPort.Port
				}
			case util.IntstrString:
				if epPort.Name == servicePort.TargetPort.StrVal {
					targetPort = epPort.Port
				}
			}
			if targetPort == 0 {
				continue
			}
			for _, epAddress := range ss.Addresses {
				endpoints = append(endpoints, fmt.Sprintf("%v:%v", epAddress.IP, targetPort))
			}
		}
	}
	return
}

// Implementation of RuleGetter interface
func (lbc *HaproxyController) getRules(s *api.Service, servicePort int) *LBRule {
	prefix := ""
	if !strings.EqualFold(s.Namespace, api.NamespaceDefault) {
		prefix = "/" + s.Namespace
	}

	port := ""
	if servicePort != 80 {
		port = ":" + strconv.Itoa(servicePort)
	}

	return &LBRule{
		ACL:  fmt.Sprintf("%v_%v%v", s.Namespace, s.Name, port),
		URLs: []string{fmt.Sprintf("%v/%v%v", prefix, s.Name, port)},
	}
}
