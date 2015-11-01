package main

import (
	"github.com/golang/glog"
	"k8s.io/kubernetes/pkg/api"
)

type IngressController struct {
	LoadBalancerController
}

func (lbc *IngressController) getRules(s *api.Service, servicePort int) *LBRule {
	glog.Infof("Obtaining rules from Ingress")
	return &LBRule{
		ACL:  "",
		URLs: []string{},
	}
}
