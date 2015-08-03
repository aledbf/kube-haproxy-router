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
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"text/tabwriter"

	"github.com/aledbf/kube-haproxy-router/cluster"
	"github.com/golang/glog"
)

func (h *HAProxyManager) writeHTTPFrontend(services []cluster.Service, writer io.Writer) {
	fmt.Fprintf(writer, "frontend www\n")
	fmt.Fprintf(writer, "\tbind :%d\n", 80)
	fmt.Fprintf(writer, "\ttcp-request inspect-delay 5s\n")
  fmt.Fprintf(writer, "\ttcp-request content accept if HTTP\n")
	for _, record := range services {
		if record.RealName != "" {
			fmt.Fprintf(writer, "\tacl acl_%s\thdr(host) %s.%s\n", record.RealName, record.RealName, h.DomainName)
		}
	}

	fmt.Fprint(writer, "\n")

	for _, record := range services {
		if record.RealName != "" {
			fmt.Fprintf(writer, "\tuse_backend\t%s\tif acl_%s\n", record.RealName, record.RealName)
		}
	}
	fmt.Fprintf(writer, "\n")
}

func (h *HAProxyManager) writeHTTPBackend(record cluster.Service, writer io.Writer) {
	fmt.Fprintf(writer, "backend %s\n", record.RealName)
	for _, subset := range record.Ep {
		fmt.Fprintf(writer, "\tserver %s\t%s:%d\tcheck\n", subset.String(), subset.Host, subset.Port)
	}
	fmt.Fprintf(writer, "\n")
}

func (h *HAProxyManager) updateHAProxy(services []cluster.Service, dryrun bool) error {
	rb := bytes.NewBuffer(nil)
	wb := bytes.NewBuffer(nil)
	buf := bufio.NewReadWriter(bufio.NewReader(rb), bufio.NewWriter(wb))

	w := new(tabwriter.Writer)
	w.Init(buf, 1, 8, 1, '\t', tabwriter.TabIndent)

	h.writeConfig(services, w)
	buf.Flush()

	if dryrun {
		glog.Info(string(wb.Bytes()))
	} else {
		writer, err := os.OpenFile(h.ConfigFile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			return err
		}
		defer writer.Close()
		writer.Write(wb.Bytes())
	}

	return nil
}

func (h *HAProxyManager) writeConfig(services []cluster.Service, writer io.Writer) {
	io.WriteString(writer, header)
	io.WriteString(writer, "\n")
	if len(services) > 0 {
		h.writeHTTPFrontend(services, writer)
		for _, serviceRecord := range services {
			h.writeHTTPBackend(serviceRecord, writer)
		}
	}
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

func (h *HAProxyManager) Sync(services []cluster.Service, dryrun bool) error {
	err := h.updateHAProxy(services, dryrun)
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
