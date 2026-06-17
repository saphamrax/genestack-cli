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

func TestScaleKeyRejectsController(t *testing.T) {
	c := model.NewCluster("lab")
	c.Nodes = []model.Node{
		{Name: "ctl01", AnsibleHost: "10.0.0.51", Roles: []model.Role{model.RoleController}},
		{Name: "cmp01", AnsibleHost: "10.0.0.54", Roles: []model.Role{model.RoleCompute}},
	}
	m := New(c, "cluster.yaml", "")
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	// Focus the controller node and press 's' — it must be rejected, not started.
	m.focus = focusNodes
	m.nodeIdx = 0
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s")})
	if m.running {
		t.Fatal("scale should not start for a controller node")
	}
	if !strings.Contains(m.status, "controller") {
		t.Errorf("expected a controller-unsupported status, got %q", m.status)
	}
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
