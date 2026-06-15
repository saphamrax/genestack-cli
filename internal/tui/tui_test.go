package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rackerlabs/genestack-cli/internal/model"
)

func TestViewRendersAfterResize(t *testing.T) {
	c := model.NewCluster("lab")
	c.Deployment.Host = "10.0.0.10"
	c.Nodes = []model.Node{
		{Name: "ctl01", AnsibleHost: "10.0.0.51", Roles: []model.Role{model.RoleController}},
		{Name: "cmp01", AnsibleHost: "10.0.0.54", Roles: []model.Role{model.RoleCompute}},
	}
	m := New(c, "cluster.yaml", "")

	// Before a size is known, View must not panic.
	if got := m.View(); got == "" {
		t.Fatal("empty view before resize")
	}

	// Feed a window size and render the full dashboard.
	model, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	out := model.View()
	for _, want := range []string{"Nodes", "Phases", "Logs", "ctl01"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q", want)
		}
	}

	// Tiny terminal should not panic either.
	model, _ = model.Update(tea.WindowSizeMsg{Width: 20, Height: 10})
	_ = model.View()
}

func TestExpandPlaceholders(t *testing.T) {
	c := model.NewCluster("lab")
	c.Region = "RegionTwo"
	c.Domain = "api.example.net"
	m := New(c, "cluster.yaml", "")
	got := m.expand("create-secrets.sh --region {{REGION}} --domain {{DOMAIN}}")
	want := "create-secrets.sh --region RegionTwo --domain api.example.net"
	if got != want {
		t.Errorf("expand = %q, want %q", got, want)
	}
}
