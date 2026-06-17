package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/rackerlabs/genestack-cli/internal/model"
)

// writeCluster saves a minimal cluster.yaml in a temp dir and returns its path.
func writeCluster(t *testing.T, nodes ...model.Node) string {
	t.Helper()
	c := model.NewCluster("scaletest")
	c.Nodes = append([]model.Node{
		{Name: "ctl01", AnsibleHost: "10.0.0.51", NodeIP: "10.0.0.51", Roles: []model.Role{model.RoleController}},
	}, nodes...)
	path := filepath.Join(t.TempDir(), "cluster.yaml")
	if err := c.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	return path
}

func TestScaleAddRejectsController(t *testing.T) {
	path := writeCluster(t)
	err := cmdScaleAdd(path, []string{"ctl02", "--ip", "10.0.0.52", "--roles", "controller"})
	if err == nil || !strings.Contains(err.Error(), "controller") {
		t.Fatalf("expected controller rejection, got %v", err)
	}
}

func TestScaleAddDefaultsToCompute(t *testing.T) {
	path := writeCluster(t)
	if err := cmdScaleAdd(path, []string{"cmp07", "--ip", "10.0.0.87"}); err != nil {
		t.Fatalf("scale add: %v", err)
	}
	c, err := model.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	n := c.Node("cmp07")
	if n == nil || !n.IsCompute() {
		t.Fatalf("expected cmp07 added with compute role, got %+v", n)
	}
}

func TestScaleApplyValidatesNodes(t *testing.T) {
	path := writeCluster(t, model.Node{Name: "cmp07", AnsibleHost: "10.0.0.87", Roles: []model.Role{model.RoleCompute}})

	// Unknown node is rejected (dry-run, so no connection is attempted).
	if err := cmdScaleApply(path, []string{"nope", "--dry-run"}); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected unknown node to be rejected, got %v", err)
	}
	// Controller node is rejected for scaling.
	if err := cmdScaleApply(path, []string{"ctl01", "--dry-run"}); err == nil || !strings.Contains(err.Error(), "controller") {
		t.Fatalf("expected controller to be rejected, got %v", err)
	}
	// A valid compute node passes validation and dry-runs cleanly.
	if err := cmdScaleApply(path, []string{"cmp07", "--dry-run"}); err != nil {
		t.Fatalf("expected valid compute node to dry-run, got %v", err)
	}
}
