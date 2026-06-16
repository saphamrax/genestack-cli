package model

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// validEndpointHostKeys is the set of service keys accepted under
// overrides.endpoints.hosts. Keep in sync with the service prefixes used by the
// overrides package (host() callers) plus the gateway-only services.
var validEndpointHostKeys = map[string]bool{
	"nova": true, "metadata": true, "novnc": true, "cloudformation": true,
	"cloudwatch": true, "magnum": true, "barbican": true, "horizon": true,
	"gnocchi": true, "glance": true, "octavia": true, "neutron": true,
	"heat": true, "placement": true, "cinder": true, "keystone": true,
	"grafana": true, "masakari": true, "skyline": true, "harbor": true,
}

func sortedMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ValidationReport collects all problems found while checking a Cluster.
// Errors are blocking (the config cannot be deployed as-is); Warnings flag
// likely-unintended or incomplete settings that do not prevent generation.
type ValidationReport struct {
	Errors   []string
	Warnings []string
}

// HasErrors reports whether any blocking problems were found.
func (r *ValidationReport) HasErrors() bool { return len(r.Errors) > 0 }

func (r *ValidationReport) addError(format string, a ...any) {
	r.Errors = append(r.Errors, fmt.Sprintf(format, a...))
}

func (r *ValidationReport) addWarning(format string, a ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, a...))
}

// Check runs all structural checks against the cluster and returns every
// problem found (it does not stop at the first one). It does not parse IPs or
// CIDRs — only structural invariants: presence, uniqueness and valid roles.
func (c *Cluster) Check() *ValidationReport {
	r := &ValidationReport{}

	if strings.TrimSpace(c.Name) == "" {
		r.addWarning("cluster has no name")
	}
	if strings.TrimSpace(c.Region) == "" {
		r.addWarning("region is empty")
	}
	if d := strings.TrimSpace(c.Domain); d == "" {
		r.addWarning("domain is empty")
	} else if strings.Contains(d, "example.com") {
		r.addWarning("domain %q looks like a placeholder", d)
	}

	if !c.Deployment.IsLocal() {
		if strings.TrimSpace(c.Deployment.Host) == "" {
			r.addError("deployment.host is required in ssh mode")
		}
		if strings.TrimSpace(c.Deployment.User) == "" {
			r.addError("deployment.user is required in ssh mode")
		}
	}
	if kp := c.Deployment.KeyPath; kp != "" {
		if _, err := os.Stat(kp); err != nil {
			r.addWarning("deployment.key_path %q is not readable", kp)
		}
	}

	for _, svc := range sortedMapKeys(c.Overrides.Endpoints.Hosts) {
		fqdn := c.Overrides.Endpoints.Hosts[svc]
		if !validEndpointHostKeys[svc] {
			r.addWarning("overrides.endpoints.hosts: unknown service %q (ignored)", svc)
		}
		if strings.ContainsAny(fqdn, "/: ") {
			r.addWarning("overrides.endpoints.hosts[%s] = %q should be a bare hostname (no scheme/port/path)", svc, fqdn)
		}
	}

	if len(c.Nodes) == 0 {
		r.addError("no nodes defined")
	}
	if len(c.Nodes) > 0 && len(c.GroupMembers(GroupKubeControlPlane)) == 0 {
		r.addError("at least one controller node is required")
	}

	names := map[string]bool{}
	hosts := map[string]bool{}
	nodeIPs := map[string]bool{}
	for i, n := range c.Nodes {
		// A node with no name can't be referenced in error messages by name.
		label := n.Name
		if label == "" {
			label = fmt.Sprintf("nodes[%d]", i)
			r.addError("%s: missing name", label)
		} else if names[n.Name] {
			r.addError("duplicate node name %q", n.Name)
		}
		names[n.Name] = true

		if strings.TrimSpace(n.AnsibleHost) == "" {
			r.addError("node %q: missing ansible_host", label)
		} else if hosts[n.AnsibleHost] {
			r.addError("duplicate ansible_host %q", n.AnsibleHost)
		}
		hosts[n.AnsibleHost] = true

		if n.NodeIP != "" {
			if nodeIPs[n.NodeIP] {
				r.addError("duplicate node_ip %q", n.NodeIP)
			}
			nodeIPs[n.NodeIP] = true
		} else {
			r.addWarning("node %q: no node_ip (kubespray will use ansible_host)", label)
		}

		if len(n.Roles) == 0 {
			r.addError("node %q has no roles", label)
		}
		for _, role := range n.Roles {
			if !ValidRole(role) {
				r.addError("node %q: invalid role %q", label, role)
			}
		}
	}

	return r
}
