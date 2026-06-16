package runner

import (
	"context"
	"strings"
	"testing"

	"github.com/rackerlabs/genestack-cli/internal/engine"
	"github.com/rackerlabs/genestack-cli/internal/model"
)

func TestRunStepValidateNetworks(t *testing.T) {
	c := rebootCluster()
	c.K8s.KubeServiceCIDR = "10.233.0.0/18"
	c.K8s.KubePodsSubnet = "10.233.64.0/18"
	c.Nodes[0].NodeIP = "10.239.44.51"
	c.Nodes[1].NodeIP = "10.239.44.54"
	step := engine.Step{ID: "k8s.preflight", Action: engine.ActionValidateNetworks}

	// Valid CIDRs: the action passes (no executor needed).
	r := &Runner{Cluster: c}
	if err := r.RunStep(context.Background(), step, nil); err != nil {
		t.Fatalf("expected valid CIDRs to pass, got %v", err)
	}

	// Pod CIDR that swallows the node IPs must block the deploy.
	c.K8s.KubePodsSubnet = "10.236.0.0/14"
	if err := r.RunStep(context.Background(), step, nil); err == nil {
		t.Fatal("expected overlapping pod CIDR to fail the preflight")
	}
}

func rebootCluster() *model.Cluster {
	c := model.NewCluster("lab")
	c.Nodes = []model.Node{
		{Name: "ctl01", AnsibleHost: "10.0.0.51", NodeIP: "10.0.0.51", Roles: []model.Role{model.RoleController}},
		{Name: "cmp01", AnsibleHost: "10.0.0.54", NodeIP: "10.0.0.54", Roles: []model.Role{model.RoleCompute}},
	}
	return c
}

func TestExpandDeployNodeWhenDeployerIsANode(t *testing.T) {
	c := rebootCluster()
	c.Deployment.Host = "10.0.0.51" // == ctl01's ansible_host
	r := &Runner{Cluster: c}
	got := r.Expand(`ansible 'all:!{{DEPLOY_NODE}}' -m reboot`)
	if !strings.Contains(got, "all:!ctl01") {
		t.Errorf("expected the deployer to be excluded, got %q", got)
	}
}

func TestExpandDeployNodeEmptyForJumpHost(t *testing.T) {
	c := rebootCluster()
	c.Deployment.Host = "172.16.0.9" // not one of the nodes
	r := &Runner{Cluster: c}
	if dn := deployNodeName(c); dn != "" {
		t.Errorf("expected empty deploy node for a standalone jump host, got %q", dn)
	}
	got := r.Expand(`x{{DEPLOY_NODE}}y`)
	if got != "xy" {
		t.Errorf("expected {{DEPLOY_NODE}} to expand to empty, got %q", got)
	}
}

func TestGroupVarsStepPinsCIDRs(t *testing.T) {
	c := rebootCluster()
	c.K8s.KubePodsSubnet = "10.233.64.0/18"
	c.K8s.KubeServiceCIDR = "10.233.0.0/18"
	r := &Runner{Cluster: c}

	var cmd string
	for _, p := range engine.DefaultPhases() {
		for _, s := range p.Steps {
			if s.ID == "config.groupvars" {
				cmd = s.Cmd
			}
		}
	}
	if cmd == "" {
		t.Fatal("config.groupvars step not found")
	}
	got := r.Expand(cmd)
	if !strings.Contains(got, "kube_pods_subnet: 10.233.64.0/18") ||
		!strings.Contains(got, "kube_service_addresses: 10.233.0.0/18") {
		t.Errorf("group_vars override missing the model CIDRs: %q", got)
	}
}

func TestExpandKubeCIDRs(t *testing.T) {
	c := rebootCluster()
	c.K8s.KubePodsSubnet = "10.233.64.0/18"
	c.K8s.KubeServiceCIDR = "10.233.0.0/18"
	r := &Runner{Cluster: c}
	got := r.Expand(`POD_CIDR: "{{KUBE_PODS_SUBNET}}" SVC_CIDR: "{{KUBE_SERVICE_CIDR}}"`)
	if !strings.Contains(got, `POD_CIDR: "10.233.64.0/18"`) || !strings.Contains(got, `SVC_CIDR: "10.233.0.0/18"`) {
		t.Errorf("CIDR placeholders not expanded: %q", got)
	}
}

func TestDeployNodeMatchesByName(t *testing.T) {
	c := rebootCluster()
	c.Deployment.Host = "ctl01" // hostname rather than IP
	if dn := deployNodeName(c); dn != "ctl01" {
		t.Errorf("expected match by name, got %q", dn)
	}
}
