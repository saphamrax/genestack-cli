package inventory

import (
	"strings"
	"testing"

	"github.com/rackerlabs/genestack-cli/internal/model"
)

func testCluster() *model.Cluster {
	c := model.NewCluster("lab01")
	c.Nodes = []model.Node{
		{Name: "ctl01", AnsibleHost: "10.0.0.51", Roles: []model.Role{model.RoleController}},
		{Name: "cmp01", AnsibleHost: "10.0.0.54", Roles: []model.Role{model.RoleCompute}},
		{Name: "net01", AnsibleHost: "10.0.0.60", Roles: []model.Role{model.RoleNetwork, model.RoleStorage}},
	}
	return c
}

func TestGroupMembers(t *testing.T) {
	c := testCluster()
	cases := map[string][]string{
		model.GroupKubeControlPlane:     {"ctl01"},
		model.GroupEtcd:                 {"ctl01"},
		model.GroupKubeNode:             {"cmp01", "ctl01", "net01"}, // every node is a worker
		model.GroupOpenStackControl:     {"ctl01"},
		model.GroupOVNNetworkNodes:      {"ctl01", "net01"},
		model.GroupCinderStorageNodes:   {"ctl01", "net01"},
		model.GroupOpenStackComputeNode: {"cmp01"},
	}
	for group, want := range cases {
		got := c.GroupMembers(group)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("group %s: got %v, want %v", group, got, want)
		}
	}
}

func TestGenerateStructure(t *testing.T) {
	c := testCluster()
	b, err := Generate(c)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, want := range []string{
		"ansible_port: 22",
		"kube_ovn_central_hosts: \"{{ groups['ovn_network_nodes'] }}\"",
		"ctl01: null",
		"enable_iscsi: true", // compute group var
	} {
		if !strings.Contains(out, want) {
			t.Errorf("inventory missing %q\n---\n%s", want, out)
		}
	}
}

func TestNodeIPEmitsIPAndAccessIP(t *testing.T) {
	c := model.NewCluster("lab")
	c.Nodes = []model.Node{
		{Name: "ctl01", AnsibleHost: "203.0.113.10", NodeIP: "192.168.100.10", Roles: []model.Role{model.RoleController}},
		{Name: "cmp01", AnsibleHost: "203.0.113.20", Roles: []model.Role{model.RoleCompute}}, // no node_ip
	}
	b, err := Generate(c)
	if err != nil {
		t.Fatal(err)
	}
	out := string(b)
	for _, want := range []string{
		"ansible_host: 203.0.113.10",
		"ip: 192.168.100.10",
		"access_ip: 192.168.100.10",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("inventory missing %q", want)
		}
	}
	// A node without node_ip must not emit ip/access_ip for itself.
	if strings.Contains(out, "ansible_host: 203.0.113.20\n      ip:") {
		t.Error("cmp01 should not have an ip line (no node_ip set)")
	}
}

func TestGenerateRequiresController(t *testing.T) {
	c := model.NewCluster("lab")
	c.Nodes = []model.Node{{Name: "cmp01", AnsibleHost: "10.0.0.1", Roles: []model.Role{model.RoleCompute}}}
	if _, err := Generate(c); err == nil {
		t.Fatal("expected error when no controller node is present")
	}
}
