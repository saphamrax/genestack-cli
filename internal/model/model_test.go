package model

import (
	"strings"
	"testing"
)

// validCluster returns a cluster that passes Check() with no errors or warnings.
func validCluster() *Cluster {
	c := NewCluster("lab")
	c.Domain = "api.lab.internal" // avoid the example.com placeholder warning
	c.Deployment = Deployment{Mode: "ssh", Host: "10.0.0.10", User: "root", Port: 22}
	c.Nodes = []Node{
		{Name: "ctl01", AnsibleHost: "10.0.0.51", NodeIP: "10.0.0.51", Roles: []Role{RoleController}},
		{Name: "cmp01", AnsibleHost: "10.0.0.54", NodeIP: "10.0.0.54", Roles: []Role{RoleCompute}},
	}
	return c
}

func contains(list []string, sub string) bool {
	for _, s := range list {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func TestCheckValid(t *testing.T) {
	r := validCluster().Check()
	if r.HasErrors() {
		t.Fatalf("expected no errors, got %v", r.Errors)
	}
	if len(r.Warnings) != 0 {
		t.Fatalf("expected no warnings, got %v", r.Warnings)
	}
}

func TestCheckErrors(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Cluster)
		want   string
	}{
		{"no nodes", func(c *Cluster) { c.Nodes = nil }, "no nodes defined"},
		{"no controller", func(c *Cluster) {
			c.Nodes[0].Roles = []Role{RoleCompute}
		}, "controller node is required"},
		{"duplicate name", func(c *Cluster) {
			c.Nodes[1].Name = c.Nodes[0].Name
		}, "duplicate node name"},
		{"duplicate ansible_host", func(c *Cluster) {
			c.Nodes[1].AnsibleHost = c.Nodes[0].AnsibleHost
		}, "duplicate ansible_host"},
		{"duplicate node_ip", func(c *Cluster) {
			c.Nodes[1].NodeIP = c.Nodes[0].NodeIP
		}, "duplicate node_ip"},
		{"missing ansible_host", func(c *Cluster) {
			c.Nodes[1].AnsibleHost = ""
		}, "missing ansible_host"},
		{"missing name", func(c *Cluster) {
			c.Nodes[1].Name = ""
		}, "missing name"},
		{"no roles", func(c *Cluster) {
			c.Nodes[1].Roles = nil
		}, "has no roles"},
		{"invalid role", func(c *Cluster) {
			c.Nodes[1].Roles = []Role{"gateway"}
		}, "invalid role"},
		{"ssh missing host", func(c *Cluster) {
			c.Deployment.Host = ""
		}, "deployment.host is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validCluster()
			tc.mutate(c)
			r := c.Check()
			if !contains(r.Errors, tc.want) {
				t.Errorf("expected an error containing %q, got %v", tc.want, r.Errors)
			}
		})
	}
}

func TestCheckWarnings(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Cluster)
		want   string
	}{
		{"missing node_ip", func(c *Cluster) {
			c.Nodes[1].NodeIP = ""
		}, "no node_ip"},
		{"placeholder domain", func(c *Cluster) {
			c.Domain = "api.openstack.example.com"
		}, "placeholder"},
		{"empty domain", func(c *Cluster) {
			c.Domain = ""
		}, "domain is empty"},
		{"unknown host key", func(c *Cluster) {
			c.Overrides.Endpoints.Hosts = map[string]string{"nonsense": "x.example.net"}
		}, "unknown service"},
		{"host with scheme", func(c *Cluster) {
			c.Overrides.Endpoints.Hosts = map[string]string{"keystone": "https://keystone.example.net"}
		}, "bare hostname"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validCluster()
			tc.mutate(c)
			r := c.Check()
			if r.HasErrors() {
				t.Fatalf("expected no errors, got %v", r.Errors)
			}
			if !contains(r.Warnings, tc.want) {
				t.Errorf("expected a warning containing %q, got %v", tc.want, r.Warnings)
			}
		})
	}
}

// Validate stays the fail-fast gate used by inventory generation.
func TestValidateStillGuards(t *testing.T) {
	c := validCluster()
	c.Nodes[0].Roles = []Role{RoleCompute} // remove the only controller
	if err := c.Validate(); err == nil {
		t.Fatal("expected Validate to error when no controller is present")
	}
	if err := validCluster().Validate(); err != nil {
		t.Fatalf("expected valid cluster to pass Validate, got %v", err)
	}
}
