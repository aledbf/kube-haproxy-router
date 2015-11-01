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

package haproxy

import (
	"io"
	"io/ioutil"
	"os"
	"text/template"

	"github.com/golang/glog"
	"k8s.io/kubernetes/pkg/util/exec"
)

const (
	haproxyCfg = "/etc/haproxy/haproxy.cfg"
	pidFile    = "/var/run/haproxy.pid"
)

// LoadBalancer represents loadbalancer specific configuration.
type LoadBalancer struct {
	Exec        exec.Interface
	Name        string `description:"Name of the load balancer, eg: haproxy."`
	StartSyslog bool   `description:"indicates if the load balancer uses syslog."`
	Algorithm   string `description:"default load balancer algorithm".`
}

func (h *LoadBalancer) buildConf(dryRun bool) error {
	var w io.Writer
	var err error
	if dryRun {
		w = os.Stdout
	} else {
		w, err = os.Create(haproxyCfg)
		if err != nil {
			return err
		}
	}

	t, err := template.ParseFiles("template.tmpl")
	if err != nil {
		return err
	}

	return t.Execute(w, h.cfg)
}

func (h *LoadBalancer) Reload() error {
	pid, err := ioutil.ReadFile(pidFile)
	if err != nil {
		if os.IsNotExist(err) {
			return h.Start()
		}
		return err
	}

	return h.runCommandAndLog("haproxy", "-f", "-p", pidFile, "-st", string(pid))
}

func (h *LoadBalancer) Start() error {
	if h.cfg.startSyslog {
		_, err := newSyslogServer("/var/run/haproxy.log.socket")
		if err != nil {
			glog.Fatalf("Failed to start syslog server: %v", err)
		}
	}

	return h.runCommandAndLog("haproxy", "-f", haproxyCfg, "-p", pidFile)
}

func (h *LoadBalancer) Check() error {
	return h.runCommandAndLog("haproxy", "-f", haproxyCfg, "-c")
}

func (h *LoadBalancer) Sync(dryrun bool) error {
	err := h.buildConf(dryrun)
	if err != nil {
		return err
	}

	err = h.Check()
	if err != nil {
		return err
	}

	if dryrun {
		return nil
	}

	err = h.Reload()
	if err != nil {
		return err
	}

	return nil
}

func (h *LoadBalancer) runCommandAndLog(cmd string, args ...string) error {
	data, err := h.Exec.Command(cmd, args...).CombinedOutput()
	if err != nil {
		glog.Warningf("Failed to run: %s %v", cmd, args)
		glog.Warning(string(data))
		return err
	}

	return nil
}
