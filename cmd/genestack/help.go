package main

import (
	"fmt"
	"sort"
	"strings"
)

// overviewHelp is the top-level help shown by `genestack help`, `genestack -h`,
// and on an unknown command.
func overviewHelp() string {
	return `genestack — TUI/CLI to deploy OpenStack via Genestack

USAGE
  genestack [--config FILE] [--local] <command> [args]
  genestack                              # no command launches the TUI
  genestack help [command]               # detailed help for a command

GLOBAL FLAGS
  --config FILE   path to the cluster config (default: cluster.yaml,
                  or $GENESTACK_CONFIG). Run state lives next to it in
                  .<name>.state.json
  --local         run commands on THIS host instead of over SSH
                  (same as deployment.mode: local in the config).
                  Must come before the command: genestack --local run --all

EXECUTION MODES
  ssh    (default) drive a remote deployment host from your laptop
  local  run directly on the deployment node (the host with /opt/genestack)

SETUP COMMANDS
  init                  create a starter cluster.yaml
  node                  add / list / remove nodes
  inventory             print / write / upload the generated inventory.yaml
  overrides             manage generated + passthrough override files
  connect               test connectivity to the deployment host

DEPLOY COMMANDS
  phases                list deployment phases with status
  steps [PHASE]         list steps (optionally for one phase) with status
  status                summarise run progress
  run TARGET...         run phases/steps on the deployment host
  reset TARGET...       clear run state so steps run again
  tui                   launch the terminal UI (default when no command)

TARGETS (for run / reset)
  A TARGET is a phase id (e.g. kubernetes) or a step id (e.g. k8s.cluster).
  List them with 'genestack phases' and 'genestack steps'.

QUICK START
  genestack init                         # write cluster.yaml, then edit it
  genestack connect                      # verify connectivity
  genestack run --all --dry-run          # preview the whole sequence
  genestack run --all                    # deploy (resumes, skips done steps)
  genestack                              # or drive it from the TUI

EXAMPLES
  genestack run config host-setup        # run two phases in order
  genestack run kubernetes --from kube-ovn   # from one phase to the end
  genestack run k8s.cluster              # run a single step
  genestack run os.octavia               # run an optional step (by name)
  genestack reset kubernetes             # re-arm a phase to run again

ENVIRONMENT
  GENESTACK_CONFIG   default value for --config

DOCS
  Full guide: docs/DEPLOYMENT.md
  Run 'genestack help <command>' for details on any command.
`
}

// commandHelp holds detailed per-command help, keyed by canonical command name.
var commandHelp = map[string]string{
	"init": `genestack init — create a starter cluster.yaml

USAGE
  genestack [--config FILE] init

DESCRIPTION
  Writes a cluster.yaml populated with sensible defaults (region, k8s CIDRs,
  MetalLB pools, OVN annotations, override knobs) and a small sample of nodes.
  Edit it afterwards: set deployment.host, domain, and your real nodes.
  Fails if the file already exists.

EXAMPLE
  genestack init
  genestack --config prod.yaml init
`,

	"node": `genestack node — manage nodes in the cluster config

USAGE
  genestack node add NAME --ip IP [--node-ip IP] [--roles r1,r2]
  genestack node list
  genestack node rm NAME

DESCRIPTION
  add   append a node. NAME is the inventory hostname; --ip is the address
        reached over SSH (ansible_host, e.g. a WAN IP). --node-ip is the
        private cluster IP kubespray binds k8s to (ip/access_ip) — set it when
        SSH and cluster traffic use different networks. --roles is a
        comma-separated list (default: controller). Roles map to inventory
        groups:
          controller  kube_control_plane, etcd, kube_node,
                      openstack_control_plane, ovn_network_nodes,
                      cinder_storage_nodes
          compute     kube_node, openstack_compute_nodes
          network     kube_node, ovn_network_nodes
          storage     kube_node, cinder_storage_nodes
        (every node is always a kube_node.)
  list  print configured nodes (name, ip, roles)
  rm    remove a node by name

EXAMPLES
  genestack node add ctl01 --ip 10.0.0.51 --roles controller
  genestack node add cmp01 --ip 10.0.0.54 --roles compute
  genestack node add ctl01 --ip 203.0.113.5 --node-ip 192.168.100.5 --roles controller
  genestack node add stor1 --ip 10.0.0.60 --roles network,storage
  genestack node list
  genestack node rm cmp01
`,

	"inventory": `genestack inventory — generate the Ansible/kubespray inventory

USAGE
  genestack inventory [--upload] [--output FILE]

DESCRIPTION
  Renders inventory.yaml from the nodes and roles in cluster.yaml.
  With no flags it prints to stdout.

FLAGS
  --upload        upload to the deployment host at
                  /etc/genestack/inventory/inventory.yaml instead of printing
  --output FILE   write to a local file instead of stdout

NOTE
  The 'run config' phase also uploads the inventory (step config.inventory),
  so you rarely need --upload manually.

EXAMPLES
  genestack inventory
  genestack inventory --output inventory.yaml
  genestack inventory --upload
`,

	"overrides": `genestack overrides — manage helm-override / manifest files

USAGE
  genestack overrides list
  genestack overrides show PATH
  genestack overrides generate [--output-dir DIR]
  genestack overrides upload

DESCRIPTION
  The CLI manages override files in two layers:
    generated     domain-derived + typed-knob files rendered from cluster.yaml
                  (endpoints, MetalLB pools, neutron MTU, nova, prometheus,
                  grafana, blackbox probes, barbican host)
    passthrough   any file you place under the local overrides/ directory,
                  mirroring the remote layout, e.g.
                  overrides/helm-configs/cinder/cinder.yaml ->
                  /etc/genestack/helm-configs/cinder/cinder.yaml
  On a path clash, passthrough wins over generated. Use passthrough for
  Ceph/NetApp/S3 backends and any secret-bearing config.

SUBCOMMANDS
  list                     show the planned files and their source
  show PATH                print a planned file (match by path suffix,
                           e.g. 'endpoints.yaml')
  generate [--output-dir]  write generated files locally to review/edit
                           (default dir: overrides-preview)
  upload                   push the merged (generated + passthrough) set to
                           the deployment host

NOTE
  The 'run config' phase also uploads overrides (step config.overrides).

EXAMPLES
  genestack overrides list
  genestack overrides show endpoints.yaml
  genestack overrides generate --output-dir review
  genestack overrides upload
`,

	"connect": `genestack connect — test connectivity to the deployment host

USAGE
  genestack [--local] connect

DESCRIPTION
  In ssh mode, dials the deployment host and authenticates (using
  deployment.key_path if set, otherwise the SSH agent). In local mode, confirms
  commands will run on this host. Use it to validate config before deploying.

EXAMPLES
  genestack connect
  genestack --local connect
`,

	"phases": `genestack phases — list deployment phases with status

USAGE
  genestack phases

DESCRIPTION
  Prints the ordered phases, each with an aggregate status icon and a
  done/total step count. Status icons: · pending  > running  ✓ done
  ✗ failed  - skipped.

EXAMPLE
  genestack phases
`,

	"steps": `genestack steps — list steps with status

USAGE
  genestack steps [PHASE]

DESCRIPTION
  Lists every step and its status, grouped by phase. Pass a PHASE id to show
  only that phase. Step ids (e.g. k8s.cluster) are the targets you pass to
  run / reset. Steps marked (optional) are skipped on a phase/--all run.

EXAMPLES
  genestack steps
  genestack steps kubernetes
`,

	"status": `genestack status — summarise run progress

USAGE
  genestack status

DESCRIPTION
  Prints a one-line summary: how many steps are done / running / failed out of
  the total. Progress is read from the state file next to cluster.yaml.

EXAMPLE
  genestack status
`,

	"run": `genestack run — run phases/steps on the deployment host

USAGE
  genestack [--local] run TARGET... [flags]

DESCRIPTION
  Executes the selected phases/steps in their canonical order, connecting to
  the deployment host first. Progress is persisted, so completed steps are
  skipped on a re-run (use --force to override). On a step failure the run
  stops; fix the cause and run again to resume.

  A TARGET is a phase id (e.g. kubernetes) or a step id (e.g. k8s.cluster).
  Optional steps are skipped during phase/--all runs unless named directly or
  --optional is given.

FLAGS
  --all            run every phase
  --from PHASE     start at this phase
  --to PHASE       stop after this phase
  --force          re-run steps already marked done
  --optional       include optional steps
  --dry-run        print the exact commands without running them
  --log-dir DIR    where to write run logs (default: <config dir>/logs)
  --no-log         do not write run logs to disk

LOGS
  Each run writes to <log-dir>/<timestamp>/: one <step-id>.log per step plus a
  combined run.log. (The TUI logs there too.)

PIPELINE (see 'genestack phases')
  config -> host-setup -> kubernetes -> kube-ovn -> metallb -> openstack-ns
  -> longhorn -> monitoring -> gateway -> infra -> ovn-config -> openstack -> post

EXAMPLES
  genestack run --all                    # whole deployment, resuming
  genestack run --all --dry-run          # preview everything
  genestack run config host-setup        # two phases
  genestack run --from kube-ovn --to metallb
  genestack run k8s.cluster              # one step
  genestack run os.octavia               # an optional step
  genestack run kubernetes --force       # re-run a completed phase
  genestack --local run config           # run on this host (no SSH)
`,

	"reset": `genestack reset — clear run state so steps run again

USAGE
  genestack reset TARGET... [--all] [--from PHASE] [--to PHASE]

DESCRIPTION
  Marks the selected steps as pending (deletes their saved status) so the next
  run executes them again. Does not touch the deployment host — it only edits
  local run state.

FLAGS
  --all            reset every step
  --from PHASE     start at this phase
  --to PHASE       stop after this phase

EXAMPLES
  genestack reset kubernetes             # re-arm a whole phase
  genestack reset k8s.cluster            # re-arm one step
  genestack reset --all                  # start over
`,

	"tui": `genestack tui — launch the terminal UI

USAGE
  genestack [--local] tui
  genestack                              # tui is the default command

DESCRIPTION
  Opens a three-pane dashboard (nodes / phases / live logs). Keys:
    tab        switch focus (nodes <-> phases)
    up/down    navigate
    enter      run the selected phase
    R          run all phases from the selected one onward
    c          connect to the deployment host
    g          generate & upload the inventory
    o          generate & upload the override files
    a / d      add / delete a node
    pgup/pgdn  scroll logs
    q          quit

EXAMPLE
  genestack
  genestack tui
`,
}

// helpAliases maps command aliases to their canonical name for help lookup.
var helpAliases = map[string]string{
	"inv": "inventory",
	"ov":  "overrides",
	"ui":  "tui",
}

// printHelp prints the overview (empty name) or a command's detailed help.
func printHelp(name string) {
	if name == "" {
		fmt.Print(overviewHelp())
		return
	}
	if canon, ok := helpAliases[name]; ok {
		name = canon
	}
	if h, ok := commandHelp[name]; ok {
		fmt.Print(h)
		return
	}
	names := make([]string, 0, len(commandHelp))
	for n := range commandHelp {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Printf("no help for %q. Known commands: %s\n\n", name, strings.Join(names, ", "))
	fmt.Print(overviewHelp())
}

// wantsHelp reports whether args contain a help flag.
func wantsHelp(args []string) bool {
	for _, a := range args {
		if a == "-h" || a == "--help" || a == "help" {
			return true
		}
	}
	return false
}
