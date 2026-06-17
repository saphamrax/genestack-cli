package main

import (
	"context"
	"flag"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/rackerlabs/genestack-cli/internal/engine"
	"github.com/rackerlabs/genestack-cli/internal/model"
	"github.com/rackerlabs/genestack-cli/internal/runlog"
)

// cmdScale registers new worker nodes (scale add) and onboards them into a
// running cluster (scale apply). It automates docs/add_node.md, limited to the
// named nodes. Control-plane scaling is intentionally unsupported.
func cmdScale(cfgPath string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("scale requires a subcommand: add, apply")
	}
	sub := args[0]
	args = args[1:]
	switch sub {
	case "add":
		return cmdScaleAdd(cfgPath, args)
	case "apply":
		return cmdScaleApply(cfgPath, args)
	default:
		return fmt.Errorf("unknown scale subcommand %q", sub)
	}
}

// cmdScaleAdd registers a new node in cluster.yaml. It mirrors `node add` but
// defaults --roles to compute and rejects the controller role (use the normal
// deploy flow to add control-plane nodes).
func cmdScaleAdd(cfgPath string, args []string) error {
	if len(args) < 1 || strings.HasPrefix(args[0], "-") {
		return fmt.Errorf("usage: scale add NAME --ip IP [--node-ip IP] [--roles compute]")
	}
	name := args[0]
	fs := flag.NewFlagSet("scale add", flag.ContinueOnError)
	ip := fs.String("ip", "", "ansible_host IP (reached over SSH, e.g. WAN)")
	nodeIP := fs.String("node-ip", "", "kubespray k8s node IP (private cluster/overlay IP)")
	rolesRaw := fs.String("roles", "compute", "comma-separated roles (controller not allowed)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	c, err := loadCluster(cfgPath)
	if err != nil {
		return err
	}
	var roles []model.Role
	for _, r := range strings.Split(*rolesRaw, ",") {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		if model.Role(r) == model.RoleController {
			return fmt.Errorf("scale add cannot add controller nodes; control-plane scaling is unsupported")
		}
		roles = append(roles, model.Role(r))
	}
	if err := c.AddNode(model.Node{Name: name, AnsibleHost: *ip, NodeIP: *nodeIP, Roles: roles}); err != nil {
		return err
	}
	if err := c.Save(cfgPath); err != nil {
		return err
	}
	fmt.Printf("added node %s (run: genestack scale apply %s)\n", name, name)
	return nil
}

// cmdScaleApply runs the onboarding pipeline against the named nodes.
func cmdScaleApply(cfgPath string, args []string) error {
	fs := flag.NewFlagSet("scale apply", flag.ContinueOnError)
	dry := fs.Bool("dry-run", false, "print commands without running them")
	optional := fs.Bool("optional", false, "include optional steps (e.g. openstack-ops host config)")
	skipOS := fs.Bool("skip-os-update", false, "skip the OS package upgrade and reboot of the new nodes")
	from := fs.String("from", "", "resume at this scale step id (e.g. scale.k8s)")
	logDir := fs.String("log-dir", "", "directory for run logs (default: <config dir>/logs)")
	noLog := fs.Bool("no-log", false, "do not write run logs to disk")
	names, err := parseInterspersed(fs, args)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return fmt.Errorf("usage: scale apply NAME [NAME...] (nodes must already exist; add them with 'scale add')")
	}

	c, err := loadCluster(cfgPath)
	if err != nil {
		return err
	}
	// Validate every named node exists and is a worker (not a controller).
	for _, name := range names {
		n := c.Node(name)
		if n == nil {
			return fmt.Errorf("node %q not found in %s (add it with 'scale add')", name, cfgPath)
		}
		if n.IsController() {
			return fmt.Errorf("node %q is a controller; control-plane scaling is unsupported", name)
		}
	}

	steps := engine.ScaleSteps()
	if *from != "" {
		i := indexOfStep(steps, *from)
		if i < 0 {
			return fmt.Errorf("unknown scale step %q", *from)
		}
		steps = steps[i:]
	}

	rnr, cli := buildRunner(c, overridesDirFor(cfgPath))
	rnr.ScaleHosts = names
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
		rl.Event("scale apply: %v", names)
	}

	if !*dry {
		if err := cli.Connect(ctx); err != nil {
			return err
		}
		defer cli.Close()
	}

	for _, st := range steps {
		if st.Optional && !*optional {
			fmt.Printf("· skip %-18s (optional; use --optional)\n", st.ID)
			continue
		}
		if *skipOS && (st.ID == "scale.upgrade" || st.ID == "scale.reboot") {
			fmt.Printf("· skip %-18s (--skip-os-update)\n", st.ID)
			continue
		}
		if *dry {
			if st.Action != "" {
				fmt.Printf("[dry-run] %s\n    (action: %s)\n", st.ID, st.Action)
			} else {
				fmt.Printf("[dry-run] %s\n    %s\n", st.ID, rnr.Expand(st.Cmd))
			}
			continue
		}

		fmt.Printf("▶ %-18s %s\n", st.ID, st.Title)
		rl.Event("▶ %s — %s", st.ID, st.Title)
		stepW, closeStep := rl.Step(st.ID)
		err := rnr.RunStep(ctx, st, func(l string) {
			fmt.Println("    " + l)
			stepW(l)
		})
		closeStep()
		if err != nil {
			rl.Event("✗ %s: %v", st.ID, err)
			return fmt.Errorf("step %s failed: %w (resume with --from %s after fixing)", st.ID, err, st.ID)
		}
		rl.Event("✓ %s", st.ID)
		fmt.Printf("✓ %s\n", st.ID)
	}
	return nil
}

// indexOfStep returns the position of the step with the given id, or -1.
func indexOfStep(steps []engine.Step, id string) int {
	for i, s := range steps {
		if s.ID == id {
			return i
		}
	}
	return -1
}
