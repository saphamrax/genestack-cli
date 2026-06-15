// Package runner executes engine steps on the deployment host over SSH (or
// locally), streaming their output. Most steps run a shell command; a few are
// "actions" that generate a file from the cluster model and upload it (inventory
// and override files). The runner is the orchestration layer, so it is allowed
// to depend on the model/inventory/overrides packages; lower layers do not
// depend on it.
package runner

import (
	"context"
	"fmt"
	"strings"

	"github.com/rackerlabs/genestack-cli/internal/engine"
	"github.com/rackerlabs/genestack-cli/internal/exec"
	"github.com/rackerlabs/genestack-cli/internal/inventory"
	"github.com/rackerlabs/genestack-cli/internal/model"
	"github.com/rackerlabs/genestack-cli/internal/overrides"
)

// Runner executes commands and file-upload actions on the deployment host.
type Runner struct {
	Exec         exec.Executor
	Cluster      *model.Cluster
	OverridesDir string // local passthrough directory for override files
}

// Expand substitutes cluster-specific placeholders in a command.
func (r *Runner) Expand(cmd string) string {
	c := r.Cluster
	repl := strings.NewReplacer(
		"{{REGION}}", c.Region,
		"{{DOMAIN}}", c.Domain,
		"{{OVN_INT_BRIDGE}}", c.OVN.IntBridge,
		"{{OVN_BRIDGES}}", c.OVN.Bridges,
		"{{OVN_PORTS}}", c.OVN.Ports,
		"{{OVN_MAPPINGS}}", c.OVN.Mappings,
		"{{OVN_AZ}}", c.OVN.AvailabilityZone,
		"{{KUBE_OVN_IFACE}}", c.K8s.KubeOVNIface,
		// Exact node names by role (k8s node name == inventory hostname),
		// avoiding brittle name-substring matching in label commands.
		"{{CONTROLLER_NODES}}", nodeNames(c, model.Node.IsController),
		"{{COMPUTE_NODES}}", nodeNames(c, model.Node.IsCompute),
	)
	return repl.Replace(cmd)
}

// nodeNames returns the space-separated names of nodes matching pred.
func nodeNames(c *model.Cluster, pred func(model.Node) bool) string {
	var names []string
	for _, n := range c.Nodes {
		if pred(n) {
			names = append(names, n.Name)
		}
	}
	return strings.Join(names, " ")
}

// RunStep executes a step. For command steps it streams combined stdout/stderr
// to onLine; for action steps it generates and uploads the relevant file(s),
// reporting progress through onLine. It blocks until completion. onLine may be
// nil. The executor must already be connected.
func (r *Runner) RunStep(ctx context.Context, st engine.Step, onLine func(string)) error {
	switch st.Action {
	case engine.ActionUploadInventory:
		return r.uploadInventory(ctx, onLine)
	case engine.ActionUploadOverrides:
		return r.uploadOverrides(ctx, onLine)
	}
	return r.runCommand(ctx, r.Expand(st.Cmd), onLine)
}

func (r *Runner) runCommand(ctx context.Context, cmd string, onLine func(string)) error {
	lines := make(chan string, 128)
	done := make(chan struct{})
	go func() {
		for l := range lines {
			if onLine != nil {
				onLine(l)
			}
		}
		close(done)
	}()

	err := r.Exec.RunStream(ctx, cmd, lines)
	close(lines)
	<-done
	return err
}

func emit(onLine func(string), s string) {
	if onLine != nil {
		onLine(s)
	}
}

func (r *Runner) uploadInventory(ctx context.Context, onLine func(string)) error {
	content, err := inventory.Generate(r.Cluster)
	if err != nil {
		return err
	}
	if err := r.Exec.Upload(ctx, content, engine.RemoteInventoryPath); err != nil {
		return err
	}
	emit(onLine, "uploaded inventory → "+engine.RemoteInventoryPath)
	return nil
}

func (r *Runner) uploadOverrides(ctx context.Context, onLine func(string)) error {
	plan, err := overrides.Plan(r.Cluster, r.OverridesDir)
	if err != nil {
		return err
	}
	for _, f := range plan {
		if err := r.Exec.Upload(ctx, f.Content, f.Path); err != nil {
			return fmt.Errorf("%s: %w", f.Path, err)
		}
		emit(onLine, fmt.Sprintf("uploaded (%s) → %s", f.Source, f.Path))
	}
	emit(onLine, fmt.Sprintf("uploaded %d override file(s)", len(plan)))
	return nil
}
