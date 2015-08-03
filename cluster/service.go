package cluster

import (
  "strconv"
)

// Service encapsulates a single backend entry in the load balancer config.
// The Ep field contains the ip and ports of the pods that make up a service.
type Service struct {
  Name     string
  Ep       []HostPort
  RealName string
}

// HostPort IP address and port where a pod expose a HTTP or TCP service
type HostPort struct {
  Host string
  Port int
}

// nice HostPort output
func (this HostPort) String() string {
  return this.Host + ":" + strconv.Itoa(this.Port)
}
