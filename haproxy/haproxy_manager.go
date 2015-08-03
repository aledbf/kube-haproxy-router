package haproxy

import (
	"github.com/GoogleCloudPlatform/kubernetes/pkg/util/exec"
)

type HAProxyManager struct {
	Exec       exec.Interface
	ConfigFile string
	HTTPPort   int
	DomainName string
}
