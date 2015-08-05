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
	"io/ioutil"
	"net/http"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/GoogleCloudPlatform/kubernetes/pkg/api"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/client"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/client/cache"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/controller/framework"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/fields"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/util"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/util/exec"
	"github.com/GoogleCloudPlatform/kubernetes/pkg/util/workqueue"
	haproxy_cluster "github.com/aledbf/kube-haproxy-router/cluster"
	"github.com/aledbf/kube-haproxy-router/haproxy"
	"github.com/golang/glog"

	_ "net/http/pprof"

	flag "github.com/spf13/pflag"
)

const (
	reloadQPS    = 10.0
	resyncPeriod = 10 * time.Second
	healthzPort  = 8081
)

var (
	flags = flag.NewFlagSet("", flag.ContinueOnError)

	// keyFunc for endpoints and services.
	keyFunc = framework.DeletionHandlingMetaNamespaceKeyFunc

	// Error used to indicate that a sync is deferred because the controller isn't ready yet
	deferredSync = fmt.Errorf("Deferring sync till endpoints controller has synced.")

	dry = flags.Bool("dry", false, `if set, a single dry run of configuration
		parsing is executed. Results written to stdout.`)

	cluster = flags.Bool("use-kubernetes-cluster-service", false, `If true, use the built in kubernetes
		cluster for creating the client`)

	httpPort  = flags.Int("http-port", 80, `Port to expose http services.`)
	statsPort = flags.Int("stats-port", 1936, `Port for loadbalancer stats,
		Used in the loadbalancer liveness probe.`)

	domain = flags.String("domain", "local3.deisapp.com", "Cluster domain name")

	master = flags.String("master", "http://localhost:8080", "kube api server url")

	nodes = flags.String("nodes", "", "cluster host separated by commas")

	defaultIndex []byte
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
	clusterNodes      []string
}

// getEndpoints returns a list of <endpoint ip>:<port> for a given service/target port combination.
func (lbc *loadBalancerController) getEndpoints(
	s *api.Service, servicePort *api.ServicePort) (endpoints []haproxy_cluster.HostPort) {
	ep, err := lbc.epLister.GetServiceEndpoints(s)
	if err != nil {
		return
	}

	// The intent here is to create a union of all subsets that match a targetPort.
	// We know the endpoint already matches the service, so all pod ips that have
	// the target port are capable of service traffic for it.
	for _, ss := range ep.Subsets {
		for _, epPort := range ss.Ports {
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
				endpoints = append(endpoints, haproxy_cluster.HostPort{epAddress.IP, targetPort})
			}
		}
	}
	return
}

// getServices returns a list of services and their endpoints.
func (lbc *loadBalancerController) getServices() (httpSvc []haproxy_cluster.Service) {
	services, _ := lbc.svcLister.List()
	for _, s := range services.Items {
		if s.Spec.Type == api.ServiceTypeLoadBalancer {
			glog.Infof("Ignoring service %v, it already has a loadbalancer", s.Name)
			continue
		}
		for _, servicePort := range s.Spec.Ports {
			ep := lbc.getEndpoints(&s, &servicePort)
			if s.Labels["name"] == "" {
				continue
			}

			newSvc := haproxy_cluster.Service{
				Name:     s.Name,
				Ep:       ep,
				RealName: s.Labels["name"],
			}

			httpSvc = append(httpSvc, newSvc)
			glog.V(2).Infof("Found service: %+v", newSvc)
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
	if err := lbc.haproxy.Sync(httpSvc, lbc.clusterNodes, dryRun); err != nil {
		return err
	}

	if len(httpSvc) == 0 {
		return lbc.haproxy.Reload()
	}

	if dryRun {
		return nil
	}

	lbc.reloadRateLimiter.Accept()
	return lbc.haproxy.Reload()
}

// worker handles the work queue.
func (lbc *loadBalancerController) worker() {
	for {
		key, _ := lbc.queue.Get()
		glog.Infof("Sync triggered by service %v", key)
		if err := lbc.sync(false); err != nil {
			glog.Infof("Requeuing %v because of error: %v", key, err)
			lbc.queue.Add(key)
		} else {
			lbc.queue.Done(key)
		}
	}
}

// newLoadBalancerController creates a new controller from the given config.
func newLoadBalancerController(c *client.Client, namespace string,
	domain string, nodes []string) *loadBalancerController {
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
		domain:            domain,
		clusterNodes:      nodes,
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

// healthzServer services liveness probes.
func healthzServer() {
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		// Delegate a check to the haproxy stats service.
		response, err := http.Get(fmt.Sprintf("http://localhost:%v", *statsPort))
		if err != nil {
			glog.Infof("Error %v", err)
			w.WriteHeader(http.StatusInternalServerError)
		} else {
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				contents, err := ioutil.ReadAll(response.Body)
				if err != nil {
					glog.Infof("Error reading resonse on receiving status %v: %v", response.StatusCode, err)
				}
				glog.Infof("%v\n", string(contents))
				w.WriteHeader(response.StatusCode)
			} else {
				w.WriteHeader(200)
				w.Write([]byte("ok"))
			}
		}
	})

	http.HandleFunc("/health-check", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte("ok"))
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write(getDefaultIndex())
	})

	glog.Fatal(http.ListenAndServe(fmt.Sprintf(":%v", healthzPort), nil))
}

func getDefaultIndex() []byte {
	if len(defaultIndex) > 0 {
		return defaultIndex
	}

	defaultIndex, _ = ioutil.ReadFile("haproxy-errors/index.http")
	return defaultIndex
}

func main() {
	flags.Parse(os.Args)

	var kubeClient *client.Client

	if *cluster {
		clusterClient, err := client.NewInCluster()
		if err != nil {
			glog.Fatalf("Failed to create client: %v", err)
		}
		kubeClient = clusterClient
	} else {
		config := &client.Config{
			Host: *master,
		}

		confClient, err := client.New(config)
		if err != nil {
			glog.Fatalf("Could not create api client %v", err)
		}
		kubeClient = confClient
	}

	lbc := newLoadBalancerController(kubeClient, "default", *domain, strings.Split(*nodes, ","))

	go healthzServer()

	go lbc.epController.Run(util.NeverStop)
	go lbc.svcController.Run(util.NeverStop)

	util.Until(lbc.worker, time.Second, util.NeverStop)
}
