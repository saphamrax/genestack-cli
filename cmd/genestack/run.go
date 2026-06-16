package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/rackerlabs/genestack-cli/internal/engine"
	"github.com/rackerlabs/genestack-cli/internal/exec"
	"github.com/rackerlabs/genestack-cli/internal/inventory"
	"github.com/rackerlabs/genestack-cli/internal/model"
	"github.com/rackerlabs/genestack-cli/internal/runlog"
	"github.com/rackerlabs/genestack-cli/internal/runner"
)

// selStep pairs a step with its phase and whether it was named explicitly on
// the command line (which overrides the "skip optional" rule).
type selStep struct {
	phase    engine.Phase
	step     engine.Step
	explicit bool
}

// resolveSelection turns the run/reset arguments into an ordered list of steps.
// Precedence: --all > --from/--to range > explicit phase/step targets.
func resolveSelection(phases []engine.Phase, targets []string, all bool, from, to string) ([]selStep, error) {
	phaseIdx := map[string]int{}
	stepPhase := map[string]int{}
	for i, p := range phases {
		phaseIdx[p.ID] = i
		for _, s := range p.Steps {
			stepPhase[s.ID] = i
		}
	}

	var out []selStep

	switch {
	case all:
		for _, p := range phases {
			for _, s := range p.Steps {
				out = append(out, selStep{p, s, false})
			}
		}

	case from != "" || to != "":
		lo, hi := 0, len(phases)-1
		if from != "" {
			i, ok := phaseIdx[from]
			if !ok {
				return nil, fmt.Errorf("unknown phase %q", from)
			}
			lo = i
		}
		if to != "" {
			i, ok := phaseIdx[to]
			if !ok {
				return nil, fmt.Errorf("unknown phase %q", to)
			}
			hi = i
		}
		if lo > hi {
			return nil, fmt.Errorf("--from phase comes after --to phase")
		}
		for _, p := range phases[lo : hi+1] {
			for _, s := range p.Steps {
				out = append(out, selStep{p, s, false})
			}
		}

	case len(targets) > 0:
		want := map[string]bool{}     // step IDs to include
		explicit := map[string]bool{} // step IDs named directly
		for _, t := range targets {
			if _, ok := phaseIdx[t]; ok {
				for _, s := range phases[phaseIdx[t]].Steps {
					want[s.ID] = true
				}
				continue
			}
			if _, ok := stepPhase[t]; ok {
				want[t] = true
				explicit[t] = true
				continue
			}
			return nil, fmt.Errorf("unknown phase or step %q (see 'genestack steps')", t)
		}
		for _, p := range phases {
			for _, s := range p.Steps {
				if want[s.ID] {
					out = append(out, selStep{p, s, explicit[s.ID]})
				}
			}
		}

	default:
		return nil, fmt.Errorf("nothing selected: pass a phase/step id, or use --all / --from / --to")
	}

	return out, nil
}

// parseInterspersed parses args allowing flags and positional targets to be
// mixed in any order (Go's flag package otherwise stops at the first
// positional). It returns the positional arguments.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		positional = append(positional, args[0])
		args = args[1:]
	}
	return positional, nil
}

func buildRunner(c *model.Cluster, overridesDir string) (*runner.Runner, exec.Executor) {
	cli := exec.New(exec.Config{
		Local:                c.Deployment.IsLocal(),
		Host:                 c.Deployment.Host,
		Port:                 c.Deployment.Port,
		User:                 c.Deployment.User,
		KeyPath:              c.Deployment.KeyPath,
		AcceptUnknownHostKey: true,
	})
	return &runner.Runner{Exec: cli, Cluster: c, OverridesDir: overridesDir}, cli
}

// cmdPhases lists phases with their aggregate status.
func cmdPhases(cfgPath string) error {
	state := engine.LoadState(statePathFor(cfgPath))
	for _, p := range engine.DefaultPhases() {
		st, done, total := state.PhaseStatus(p)
		fmt.Printf("%s  %-12s %-30s %d/%d\n", st.Icon(), p.ID, p.Title, done, total)
	}
	return nil
}

// cmdSteps lists steps, optionally filtered to a single phase.
func cmdSteps(cfgPath string, args []string) error {
	filter := ""
	if len(args) > 0 {
		filter = args[0]
	}
	state := engine.LoadState(statePathFor(cfgPath))
	matched := false
	for _, p := range engine.DefaultPhases() {
		if filter != "" && p.ID != filter {
			continue
		}
		matched = true
		fmt.Printf("%s:\n", p.ID)
		for _, s := range p.Steps {
			opt := ""
			if s.Optional {
				opt = "  (optional)"
			}
			fmt.Printf("  %s  %-22s %s%s\n", state.Get(s.ID).Icon(), s.ID, s.Title, opt)
		}
	}
	if !matched {
		return fmt.Errorf("unknown phase %q", filter)
	}
	return nil
}

// cmdStatus prints a one-line summary of run progress.
func cmdStatus(cfgPath string) error {
	state := engine.LoadState(statePathFor(cfgPath))
	var done, failed, running, total int
	for _, p := range engine.DefaultPhases() {
		for _, s := range p.Steps {
			total++
			switch state.Get(s.ID) {
			case engine.StatusDone, engine.StatusSkipped:
				done++
			case engine.StatusFailed:
				failed++
			case engine.StatusRunning:
				running++
			}
		}
	}
	fmt.Printf("steps: %d done · %d running · %d failed · %d total\n", done, running, failed, total)
	return nil
}

// cmdConnect verifies SSH connectivity to the deployment host.
func cmdConnect(cfgPath string) error {
	c, err := loadCluster(cfgPath)
	if err != nil {
		return err
	}
	_, cli := buildRunner(c, overridesDirFor(cfgPath))
	if err := cli.Connect(context.Background()); err != nil {
		return err
	}
	defer cli.Close()
	if c.Deployment.IsLocal() {
		fmt.Println("local mode: commands run on this host")
	} else {
		fmt.Printf("connected to %s@%s:%d\n", c.Deployment.User, c.Deployment.Host, c.Deployment.Port)
	}
	return nil
}

// cmdRun executes selected phases/steps on the deployment host.
func cmdRun(cfgPath string, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	all := fs.Bool("all", false, "run every phase")
	from := fs.String("from", "", "start at this phase id")
	to := fs.String("to", "", "stop after this phase id")
	force := fs.Bool("force", false, "re-run steps already marked done")
	optional := fs.Bool("optional", false, "include optional steps")
	dry := fs.Bool("dry-run", false, "print commands without running them")
	logDir := fs.String("log-dir", "", "directory for run logs (default: <config dir>/logs)")
	noLog := fs.Bool("no-log", false, "do not write run logs to disk")
	targets, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}

	c, err := loadCluster(cfgPath)
	if err != nil {
		return err
	}
	state := engine.LoadState(statePathFor(cfgPath))
	phases := engine.DefaultPhases()

	sel, err := resolveSelection(phases, targets, *all, *from, *to)
	if err != nil {
		return err
	}

	rnr, cli := buildRunner(c, overridesDirFor(cfgPath))
	ctx := context.Background()

	var rl *runlog.Logger
	if !*dry && !*noLog {
		dir := *logDir
		if dir == "" {
			dir = filepath.Join(filepath.Dir(cfgPath), "logs")
		}
		rl, err = runlog.New(dir)
		if err != nil {
			return err
		}
		defer rl.Close()
		fmt.Printf("logging to %s\n", rl.Dir())
		rl.Event("run start: %v", targets)
	}

	if !*dry {
		if err := cli.Connect(ctx); err != nil {
			return err
		}
		defer cli.Close()
	}

	for _, s := range sel {
		cur := state.Get(s.step.ID)
		if (cur == engine.StatusDone || cur == engine.StatusSkipped) && !*force {
			fmt.Printf("· skip %-22s (%s)\n", s.step.ID, cur)
			continue
		}
		if s.step.Optional && !s.explicit && !*optional {
			fmt.Printf("· skip %-22s (optional; name it directly or use --optional)\n", s.step.ID)
			continue
		}
		if *dry {
			if s.step.Action != "" {
				fmt.Printf("[dry-run] %s\n    (action: %s)\n", s.step.ID, s.step.Action)
			} else {
				fmt.Printf("[dry-run] %s\n    %s\n", s.step.ID, rnr.Expand(s.step.Cmd))
			}
			continue
		}

		fmt.Printf("▶ %-22s %s\n", s.step.ID, s.step.Title)
		state.Set(s.step.ID, engine.StatusRunning)
		rl.Event("▶ %s — %s", s.step.ID, s.step.Title)
		stepW, closeStep := rl.Step(s.step.ID)
		err := rnr.RunStep(ctx, s.step, func(l string) {
			fmt.Println("    " + l)
			stepW(l)
		})
		closeStep()
		if err != nil {
			state.Set(s.step.ID, engine.StatusFailed)
			rl.Event("✗ %s: %v", s.step.ID, err)
			return fmt.Errorf("step %s failed: %w", s.step.ID, err)
		}
		state.Set(s.step.ID, engine.StatusDone)
		rl.Event("✓ %s", s.step.ID)
		fmt.Printf("✓ %s\n", s.step.ID)
	}
	return nil
}

// cmdReset clears run state for selected phases/steps so they run again.
func cmdReset(cfgPath string, args []string) error {
	fs := flag.NewFlagSet("reset", flag.ContinueOnError)
	all := fs.Bool("all", false, "reset every step")
	from := fs.String("from", "", "start at this phase id")
	to := fs.String("to", "", "stop after this phase id")
	targets, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	state := engine.LoadState(statePathFor(cfgPath))
	sel, err := resolveSelection(engine.DefaultPhases(), targets, *all, *from, *to)
	if err != nil {
		return err
	}
	for _, s := range sel {
		state.Delete(s.step.ID)
	}
	fmt.Printf("reset %d step(s)\n", len(sel))
	return nil
}

// cmdInventory prints the generated inventory and optionally uploads it.
func cmdInventory(cfgPath string, args []string) error {
	fs := flag.NewFlagSet("inventory", flag.ContinueOnError)
	upload := fs.Bool("upload", false, "upload to the deployment host instead of printing")
	output := fs.String("output", "", "write to a file instead of stdout")
	if err := fs.Parse(args); err != nil {
		return err
	}

	c, err := loadCluster(cfgPath)
	if err != nil {
		return err
	}
	b, err := inventory.Generate(c)
	if err != nil {
		return err
	}

	if *upload {
		_, cli := buildRunner(c, overridesDirFor(cfgPath))
		ctx := context.Background()
		if err := cli.Connect(ctx); err != nil {
			return err
		}
		defer cli.Close()
		if err := cli.Upload(ctx, b, engine.RemoteInventoryPath); err != nil {
			return err
		}
		fmt.Printf("uploaded inventory → %s:%s\n", c.Deployment.Host, engine.RemoteInventoryPath)
		return nil
	}

	if *output != "" {
		if err := os.WriteFile(*output, b, 0o644); err != nil {
			return err
		}
		fmt.Printf("wrote %s\n", *output)
		return nil
	}

	os.Stdout.Write(b)
	return nil
}

// cmdValidate runs structural checks on the cluster config and reports all
// problems. It exits non-zero when there are errors, or (with --strict) when
// there are warnings.
func cmdValidate(cfgPath string, args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	strict := fs.Bool("strict", false, "treat warnings as errors")
	if err := fs.Parse(args); err != nil {
		return err
	}

	c, err := loadCluster(cfgPath)
	if err != nil {
		return err
	}
	rep := c.Check()

	for _, w := range rep.Warnings {
		fmt.Printf("⚠ %s\n", w)
	}
	for _, e := range rep.Errors {
		fmt.Printf("✗ %s\n", e)
	}

	if rep.HasErrors() {
		return fmt.Errorf("validation failed: %d error(s), %d warning(s)", len(rep.Errors), len(rep.Warnings))
	}
	if *strict && len(rep.Warnings) > 0 {
		return fmt.Errorf("validation failed: %d warning(s) (--strict)", len(rep.Warnings))
	}

	controllers := len(c.GroupMembers(model.GroupKubeControlPlane))
	fmt.Printf("✓ %s: valid (%d nodes, %d controllers)\n", cfgPath, len(c.Nodes), controllers)
	return nil
}
