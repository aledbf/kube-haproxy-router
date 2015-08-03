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
	"net/http"
	"os"
	"reflect"
	"time"

	"github.com/GoogleCloudPlatform/kubernetes/pkg/api"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/client"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/client/cache"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/controller/framework"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/fields"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/util"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/util/exec"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/util/workqueue"
	"github.com/aledbf/kube-haproxy-router/haproxy"
	"github.com/aledbf/kube-haproxy-router/cluster"
	"github.com/golang/glog"

	flag "github.com/spf13/pflag"
)

const (
	reloadQPS       = 10.0
	resyncPeriod    = 10 * time.Second
	healthzPort     = ":8082"
	defaultHttpPort = 80
)

var (
	// keyFunc for endpoints and services.
	keyFunc = framework.DeletionHandlingMetaNamespaceKeyFunc

	// Error used to indicate that a sync is deferred because the controller isn't ready yet
	deferredSync = fmt.Errorf("Deferring sync till endpoints controller has synced.")
)

// loadBalancerController watches the kubernetes api and adds/removes services
// from the loadbalancer, via loadBalancerConfig.
type loadBalancerController struct {
	queue             *workqueue.Type
	client            *client.Client
	epController      *framework.Controller
	svcController     *framework.Controller
	svcLister         cache.StoreToServiceLister
	epLister          cache.StoreToEndpointsLister
	reloadRateLimiter util.RateLimiter
	template          string
	domain            string
	haproxy           *haproxy.HAProxyManager
}

// getEndpoints returns a list of <endpoint ip>:<port> for a given service/target port combination.
func (lbc *loadBalancerController) getEndpoints(
	s *api.Service, targetPort int) (endpoints []cluster.HostPort) {
	ep, err := lbc.epLister.GetServiceEndpoints(s)
	if err != nil {
		return
	}

	for _, ss := range ep.Subsets {
		for _, p := range ss.Ports {
			if p.Port == targetPort {
				for _, e := range ss.Addresses {
					endpoints = append(endpoints, cluster.HostPort{e.IP, targetPort})
				}
			}
		}
	}
	return
}

// getServices returns a list of services and their endpoints.
func (lbc *loadBalancerController) getServices() (httpSvc []cluster.Service) {
	services, _ := lbc.svcLister.List()
	for _, s := range services.Items {
		if s.Spec.Type == api.ServiceTypeLoadBalancer {
			continue
		}
		for _, servicePort := range s.Spec.Ports {
			sName := s.Name
			ep := lbc.getEndpoints(&s, servicePort.TargetPort.IntVal)
			if len(ep) == 0 {
				glog.Infof("No endpoints found for service %v, port %+v", sName, servicePort)
				continue
			}
			newSvc := cluster.Service{
				Name:     s.Name,
				Ep:       ep,
				RealName: s.Labels["name"],
			}

			httpSvc = append(httpSvc, newSvc)
			glog.Infof("Found service: %+v", newSvc)
		}
	}
	return
}

// sync all services with the loadbalancer.
func (lbc *loadBalancerController) sync(dryRun bool) error {
	if !lbc.epController.HasSynced() || !lbc.svcController.HasSynced() {
		time.Sleep(100 * time.Millisecond)
		return deferredSync
	}
	httpSvc := lbc.getServices()
	if len(httpSvc) == 0 {
		return nil
	}

	if err := lbc.haproxy.Sync(httpSvc, dryRun); err != nil {
		return err
	}

	lbc.reloadRateLimiter.Accept()
	return lbc.haproxy.Reload()
}

// worker handles the work queue.
func (lbc *loadBalancerController) worker() {
	for {
		key, _ := lbc.queue.Get()
		glog.Infof("Sync triggered by service %v", key)
		if err := lbc.haproxy.Check(); err != nil {
			glog.Infof("Requeuing %v because of error: %v", key, err)
			lbc.queue.Add(key)
		} else {
			lbc.queue.Done(key)
		}
	}
}

// newLoadBalancerController creates a new controller from the given config.
func newLoadBalancerController(c *client.Client, domain string) *loadBalancerController {
	mgr := &haproxy.HAProxyManager{
		Exec:       exec.New(),
		ConfigFile: "haproxy.cfg",
		DomainName: domain,
	}

	lbc := loadBalancerController{
		client:            c,
		queue:             workqueue.New(),
		reloadRateLimiter: util.NewTokenBucketRateLimiter(reloadQPS, int(reloadQPS)),
		haproxy:           mgr,
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
		cache.NewListWatchFromClient(lbc.client, "services", api.NamespaceAll, fields.Everything()),
		&api.Service{}, resyncPeriod, eventHandlers)

	lbc.epLister.Store, lbc.epController = framework.NewInformer(
		cache.NewListWatchFromClient(lbc.client, "endpoints", api.NamespaceAll, fields.Everything()),
		&api.Endpoints{}, resyncPeriod, eventHandlers)

	lbc.domain = domain
	return &lbc
}

// healthzServer services liveness probes.
func healthzServer() {
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	})
	glog.Fatal(http.ListenAndServe(healthzPort, nil))
}

func dryRun(lbc *loadBalancerController) {
	var err error
	for err = lbc.sync(true); err == deferredSync; err = lbc.sync(true) {
	}

	if err != nil {
		glog.Warningf("ERROR: %+v", err)
	}
}

func main() {
	flags := flag.NewFlagSet("", flag.ContinueOnError)

	cluster := flags.Bool("use-kubernetes-cluster-service", false,
		"if set, use the built in kubernetes cluster for creating the client")
	domain := flags.String("domain", "local3.deisapp.com", "Cluster domain name")
	dry := flags.Bool("dry", false,
		"if set, a single dry run of configuration parsing is executed. Results written to stdout")
	server := flags.String("server", "http://localhost:8080", "kube api server url")

	flags.Parse(os.Args)

	var c *client.Client
	if *cluster {
		clusterClient, err := client.NewInCluster()
		if err != nil {
			glog.Fatalf("Failed to create client: %v", err)
		}
		c = clusterClient
	} else {
		config := &client.Config{
			Host: *server,
		}

		client, err := client.New(config)
		if err != nil {
			glog.Fatalf("Could not create api client %v", err)
		}
		c = client
	}

	go healthzServer()

	lbc := newLoadBalancerController(c, *domain)

	go lbc.epController.Run(util.NeverStop)
	go lbc.svcController.Run(util.NeverStop)
	if *dry {
		dryRun(lbc)
	} else {
		util.Until(lbc.worker, time.Second, util.NeverStop)
	}
}
