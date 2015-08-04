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

	"github.com/aledbf/kube-haproxy-router/cluster"

	"github.com/GoogleCloudPlatform/kubernetes/pkg/util/exec"
	"github.com/golang/glog"
)

type HAProxyManager struct {
	Exec       exec.Interface
	ConfigFile string
	HTTPPort   int
	DomainName string
}

type haproxyTmpl struct {
	Services   []cluster.Service
	Nodes      []string
	DomainName string
}

func (h *HAProxyManager) buildConf(services []cluster.Service, nodes []string, dryRun bool) error {
	var w io.Writer
	var err error
	if dryRun {
		w = os.Stdout
	} else {
		w, err = os.Create(h.ConfigFile)
		if err != nil {
			return err
		}
	}

	t, err := template.ParseFiles("haproxy.tmpl")
	if err != nil {
		return err
	}

	return t.Execute(w, &haproxyTmpl{
		Services:   services,
		Nodes:      nodes,
		DomainName: h.DomainName,
	})
}

func (h *HAProxyManager) Reload() error {
	pid, err := ioutil.ReadFile("/var/run/haproxy.pid")
	if err != nil {
		if os.IsNotExist(err) {
			return h.Start()
		}
		return err
	}
	return h.runCommandAndLog("haproxy", "-f", h.ConfigFile, "-p", "/var/run/haproxy.pid", "-st", string(pid))
}

func (h *HAProxyManager) Start() error {
	return h.runCommandAndLog("haproxy", "-f", h.ConfigFile, "-p", "/var/run/haproxy.pid")
}

func (h *HAProxyManager) Check() error {
	return h.runCommandAndLog("haproxy", "-f", h.ConfigFile, "-c")
}

func (h *HAProxyManager) Sync(services []cluster.Service, nodes []string, dryrun bool) error {
	err := h.buildConf(services, nodes, dryrun)
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

func (h *HAProxyManager) runCommandAndLog(cmd string, args ...string) error {
	data, err := h.Exec.Command(cmd, args...).CombinedOutput()
	if err != nil {
		glog.Warningf("Failed to run: %s %v", cmd, args)
		glog.Warning(string(data))
		return err
	}
	return nil
}
