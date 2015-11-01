package main

// Service encapsulates a single backend entry in the load balancer config.
// The Ep field can contain the ips of the pods that make up a service, or the
// clusterIP of the service itself (in which case the list has a single entry,
// and kubernetes handles loadbalancing across the service endpoints).
type service struct {
	Name string
	Ep   []string

	// FrontendPort is the port that the loadbalancer listens on for traffic
	// for this service. For http, it's always :80, for each tcp service it
	// is the service port of any service matching a name in the tcpServices set.
	FrontendPort int

	// Host if not empty it will add a new haproxy acl to route traffic using the
	// host header inside the http request. It only applies to http traffic.
	Host string

	// Algorithm
	Algorithm string

	// If SessionAffinity is set and without CookieStickySession, requests are routed to
	// a backend based on client ip. If both SessionAffinity and CookieStickSession are
	// set, a SERVERID cookie is inserted by the loadbalancer and used to route subsequent
	// requests. If neither is set, requests are routed based on the algorithm.

	// Indicates if the service must use sticky sessions
	// http://cbonte.github.io/haproxy-dconv/configuration-1.5.html#stick-table
	// Enabled using the attribute service.spec.sessionAffinity
	// https://github.com/kubernetes/kubernetes/blob/master/docs/user-guide/services.md#virtual-ips-and-service-proxies
	SessionAffinity bool

	// CookieStickySession use a cookie to enable sticky sessions.
	// The name of the cookie is SERVERID
	// This only can be used in http services
	CookieStickySession bool

	// URLs are the paths used to route the traffic to the backend, like "<namespace>/<svc name>:<port>".
	// It requires at least one URL
	// If the namespace is "default" it will not be used.
	// If the port if is equals to 80 it will not be used.
	URLs []string
}

// serviceByName sort implementation for services
type serviceByName []service

func (s serviceByName) Len() int {
	return len(s)
}
func (s serviceByName) Swap(i, j int) {
	s[i], s[j] = s[j], s[i]
}
func (s serviceByName) Less(i, j int) bool {
	return s[i].Name < s[j].Name
}
