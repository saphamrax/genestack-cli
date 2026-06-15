package main

import (
	"strings"
	"testing"

	"github.com/rackerlabs/genestack-cli/internal/engine"
)

func ids(sel []selStep) []string {
	out := make([]string, len(sel))
	for i, s := range sel {
		out[i] = s.step.ID
	}
	return out
}

func TestResolveAll(t *testing.T) {
	phases := engine.DefaultPhases()
	sel, err := resolveSelection(phases, nil, true, "", "")
	if err != nil {
		t.Fatal(err)
	}
	var total int
	for _, p := range phases {
		total += len(p.Steps)
	}
	if len(sel) != total {
		t.Fatalf("--all selected %d, want %d", len(sel), total)
	}
}

func TestResolvePhaseAndStep(t *testing.T) {
	phases := engine.DefaultPhases()

	// A phase id selects exactly its steps, in order, none marked explicit.
	var want []string
	for _, p := range phases {
		if p.ID == "kubernetes" {
			for _, s := range p.Steps {
				want = append(want, s.ID)
			}
		}
	}
	sel, err := resolveSelection(phases, []string{"kubernetes"}, false, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(sel); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("phase select got %v, want %v", got, want)
	}
	for _, s := range sel {
		if s.explicit {
			t.Errorf("phase-selected step %s should not be explicit", s.step.ID)
		}
	}

	// A step id selects just that step, marked explicit.
	sel, err = resolveSelection(phases, []string{"k8s.cluster"}, false, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(sel) != 1 || sel[0].step.ID != "k8s.cluster" || !sel[0].explicit {
		t.Fatalf("step select got %v explicit=%v", ids(sel), sel[0].explicit)
	}
}

func TestResolveRangeOrderAndErrors(t *testing.T) {
	phases := engine.DefaultPhases()

	sel, err := resolveSelection(phases, nil, false, "kube-ovn", "metallb")
	if err != nil {
		t.Fatal(err)
	}
	got := ids(sel)
	if got[0] != "ovn.config" || got[len(got)-1] != "metallb.lb" {
		t.Fatalf("range got %v", got)
	}

	if _, err := resolveSelection(phases, nil, false, "metallb", "kube-ovn"); err == nil {
		t.Error("expected error when --from is after --to")
	}
	if _, err := resolveSelection(phases, []string{"nope"}, false, "", ""); err == nil {
		t.Error("expected error for unknown target")
	}
	if _, err := resolveSelection(phases, nil, false, "", ""); err == nil {
		t.Error("expected error when nothing is selected")
	}
}
