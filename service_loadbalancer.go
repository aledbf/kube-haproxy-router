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
	"time"

	"github.com/golang/glog"
	"github.com/openshift/origin/pkg/util/proc"
	flag "github.com/spf13/pflag"
	"k8s.io/kubernetes/pkg/api"
	"k8s.io/kubernetes/pkg/client/unversioned"
	"k8s.io/kubernetes/pkg/controller/framework"
	kubectl_util "k8s.io/kubernetes/pkg/kubectl/cmd/util"
	"k8s.io/kubernetes/pkg/util"
	"k8s.io/kubernetes/pkg/util/exec"

	"github.com/aledbf/kube-haproxy-router/haproxy"
)

var (
	flags = flag.NewFlagSet("", flag.ContinueOnError)

	// keyFunc for endpoints and services.
	keyFunc = framework.DeletionHandlingMetaNamespaceKeyFunc

	// Error used to indicate that a sync is deferred because the controller isn't ready yet
	errDeferredSync = fmt.Errorf("deferring sync till endpoints controller has synced")

	dry = flags.Bool("dry", false, `if set, a single dry run of configuration
		parsing is executed. Results written to stdout.`)

	cluster = flags.Bool("use-kubernetes-cluster-service", true, `If true, use the built in kubernetes
		cluster for creating the client`)

	// If you have pure tcp services or https services that need L3 routing, you
	// must specify them by name. Note that you are responsible for:
	// 1. Making sure there is no collision between the service ports of these services.
	//	- You can have multiple <mysql svc name>:3306 specifications in this map, and as
	//	  long as the service ports of your mysql service don't clash, you'll get
	//	  loadbalancing for each one.
	// 2. Exposing the service ports as node ports on a pod.
	// 3. Adding firewall rules so these ports can ingress traffic.
	//
	// Any service not specified in this map is treated as an http:80 service,
	// unless TargetService dictates otherwise.

	tcpServices = flags.String("tcp-services", "", `Comma separated list of tcp/https
		serviceName:servicePort pairings. This assumes you've opened up the right
		hostPorts for each service that serves ingress traffic.`)

	targetService = flags.String(
		"target-service", "", `Restrict loadbalancing to a single target service.`)

	httpPort  = flags.Int("http-port", 80, `Port to expose http services.`)
	statsPort = flags.Int("stats-port", 1936, `Port for loadbalancer stats,
		Used in the loadbalancer liveness probe.`)

	startSyslog = flags.Bool("syslog", false, `if set, it will start a syslog server
		that will forward haproxy logs to stdout.`)

	lbDefAlgorithm = flags.String("balance-algorithm", "roundrobin", `if set, it allows a custom
		default balance algorithm.`)

	allNamespaces = flags.Bool("all-namespaces", false, `if present, watch all namespaces to export services.
		Namespace in current context is ignored even if specified with --namespace.`)

	useIngress = flags.Bool("use-ingress", false, `if present, it will use Ingress to obtain the rules
		to route traffic to the services.`)
)

// registerHandlers  services liveness probes.
func registerHandlers() {
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
					glog.Infof("Error reading resonse on receiving status %v: %v",
						response.StatusCode, err)
				}
				glog.Infof("%v\n", string(contents))
				w.WriteHeader(response.StatusCode)
			} else {
				w.WriteHeader(200)
				w.Write([]byte("ok"))
			}
		}
	})

	glog.Fatal(http.ListenAndServe(fmt.Sprintf(":%v", lbApiPort), nil))
}

func main() {
	clientConfig := kubectl_util.DefaultClientConfig(flags)
	flags.Parse(os.Args)

	if len(*tcpServices) == 0 {
		glog.Infof("All tcp/https services will be ignored.")
	}

	proc.StartReaper()

	var kubeClient *unversioned.Client
	var err error

	cfg := &haproxy.Loadbalancer{
		Name:      "haproxy",
		Algorithm: *lbDefAlgorithm,
		Exec:      exec.New(),
	}

	if *startSyslog {
		cfg.StartSyslog = true
	}

	if *cluster {
		if kubeClient, err = unversioned.NewInCluster(); err != nil {
			glog.Fatalf("Failed to create client: %v", err)
		}
	} else {
		config, err := clientConfig.ClientConfig()
		if err != nil {
			glog.Fatalf("error connecting to the client: %v", err)
		}
		kubeClient, err = unversioned.New(config)
	}

	// read namespace from configuration
	namespace, specified, err := clientConfig.Namespace()
	if err != nil {
		glog.Fatalf("unexpected error: %v", err)
	}

	if !specified {
		// no namespace, we use the default
		namespace = api.NamespaceDefault
	}

	if *allNamespaces {
		// override the previous value
		namespace = api.NamespaceAll
	}

	lbc := newLoadBalancerController(cfg, kubeClient, namespace)
	if *useIngress {
		//lbc = lbc.(IngressController)
	}

	go lbc.epController.Run(util.NeverStop)
	go lbc.svcController.Run(util.NeverStop)

	if *dry {
		lbc.cfg.dryRun(lbc)
	} else {
		lbc.cfg.reload()
		util.Until(lbc.worker, time.Second, util.NeverStop)
	}
}
