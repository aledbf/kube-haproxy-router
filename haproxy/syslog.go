package haproxy

import (
	"fmt"
	"strings"

	"github.com/golang/glog"
	"github.com/ziutek/syslog"
)

type handler struct {
	*syslog.BaseHandler
}

// StartSyslogServer start a syslog server listening in the specified path
func StartSyslogServer(path string) error {
	glog.Info("Starting syslog server for haproxy")
	s := syslog.NewServer()
	s.AddHandler(newHandler())
	return s.Listen(path)
}

func newHandler() *handler {
	h := handler{syslog.NewBaseHandler(1000, nil, false)}
	go h.mainLoop()
	return &h
}

func (h *handler) mainLoop() {
	for {
		m := h.Get()
		if m == nil {
			break
		}

		fmt.Printf("kube-haproxy [%s] %s%s\n", strings.ToUpper(m.Severity.String()), m.Tag, m.Content)
	}

	h.End()
}
