package model

import (
	"strings"
	"testing"
)

func netCluster() *Cluster {
	c := NewCluster("lab")
	c.K8s.KubeServiceCIDR = "10.233.0.0/18"
	c.K8s.KubePodsSubnet = "10.233.64.0/18"
	c.Nodes = []Node{
		{Name: "ctl01", AnsibleHost: "10.239.44.51", NodeIP: "10.239.44.51", Roles: []Role{RoleController},
			Bridges: map[string]string{"br-host": "10.239.44.51", "br-overlay": "172.38.252.51"}},
		{Name: "cmp01", AnsibleHost: "10.239.44.54", NodeIP: "10.239.44.54", Roles: []Role{RoleCompute}},
	}
	return c
}

func TestCheckNetworksValid(t *testing.T) {
	if errs := netCluster().CheckNetworks(); len(errs) != 0 {
		t.Fatalf("expected no problems, got %v", errs)
	}
}

func hasSubstr(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func TestCheckNetworksOverlap(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Cluster)
		want   string
	}{
		{"pod CIDR contains node IP (lab07 bug)", func(c *Cluster) {
			c.K8s.KubePodsSubnet = "10.236.0.0/14" // spans 10.236-10.239 -> contains 10.239.44.x
		}, "is inside kube_pods_subnet"},
		{"pods overlap service", func(c *Cluster) {
			c.K8s.KubePodsSubnet = "10.233.0.0/17" // overlaps 10.233.0.0/18 service range
		}, "overlaps kube_service_addresses"},
		{"invalid pod CIDR", func(c *Cluster) {
			c.K8s.KubePodsSubnet = "not-a-cidr"
		}, "not a valid CIDR"},
		{"empty service CIDR", func(c *Cluster) {
			c.K8s.KubeServiceCIDR = ""
		}, "kube_service_addresses is required"},
		{"bridge IP inside service CIDR", func(c *Cluster) {
			c.Nodes[0].Bridges["br-overlay"] = "10.233.0.9"
		}, "is inside kube_service_addresses"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := netCluster()
			tc.mutate(c)
			errs := c.CheckNetworks()
			if !hasSubstr(errs, tc.want) {
				t.Errorf("expected a problem containing %q, got %v", tc.want, errs)
			}
		})
	}
}
