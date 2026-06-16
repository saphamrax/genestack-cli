// Command genestack is a terminal UI / CLI for deploying an OpenStack cluster
// with Genestack. It models nodes and roles, generates the Ansible/kubespray
// inventory, and drives the deployment phases over SSH to the deployment host.
//
// Usage:
//
//	genestack [--config cluster.yaml] <command>
//
// Commands:
//
//	init                 create a starter cluster.yaml
//	node add NAME ...    add a node (--ip, --roles)
//	node list            list configured nodes
//	node rm NAME         remove a node
//	inventory            print the generated inventory.yaml
//	tui                  launch the terminal UI (default)
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/rackerlabs/genestack-cli/internal/model"
	"github.com/rackerlabs/genestack-cli/internal/tui"
)

// forceLocal, set by the global --local flag, forces local execution
// regardless of deployment.mode in the config.
var forceLocal bool

func main() {
	cfgPath := flag.String("config", defaultConfigPath(), "path to cluster config file")
	local := flag.Bool("local", false, "run commands on this host instead of over SSH")
	flag.Usage = func() { fmt.Print(overviewHelp()) } // genestack -h / --help
	flag.Parse()
	forceLocal = *local
	args := flag.Args()

	cmd := "tui"
	if len(args) > 0 {
		cmd = args[0]
		args = args[1:]
	}

	// Help routing: `help [command]`, and `<command> --help`/-h.
	if cmd == "help" || cmd == "-h" || cmd == "--help" {
		name := ""
		if len(args) > 0 {
			name = args[0]
		}
		printHelp(name)
		return
	}
	if wantsHelp(args) {
		printHelp(cmd)
		return
	}

	var err error
	switch cmd {
	case "init":
		err = cmdInit(*cfgPath)
	case "node":
		err = cmdNode(*cfgPath, args)
	case "inventory", "inv":
		err = cmdInventory(*cfgPath, args)
	case "validate", "check":
		err = cmdValidate(*cfgPath, args)
	case "overrides", "ov":
		err = cmdOverrides(*cfgPath, args)
	case "phases":
		err = cmdPhases(*cfgPath)
	case "steps":
		err = cmdSteps(*cfgPath, args)
	case "status":
		err = cmdStatus(*cfgPath)
	case "run":
		err = cmdRun(*cfgPath, args)
	case "reset":
		err = cmdReset(*cfgPath, args)
	case "connect":
		err = cmdConnect(*cfgPath)
	case "tui", "ui":
		err = cmdTUI(*cfgPath)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		printHelp("")
		os.Exit(1)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func defaultConfigPath() string {
	if p := os.Getenv("GENESTACK_CONFIG"); p != "" {
		return p
	}
	return "cluster.yaml"
}

func statePathFor(cfgPath string) string {
	dir := filepath.Dir(cfgPath)
	base := strings.TrimSuffix(filepath.Base(cfgPath), filepath.Ext(cfgPath))
	return filepath.Join(dir, "."+base+".state.json")
}

func loadCluster(cfgPath string) (*model.Cluster, error) {
	if _, err := os.Stat(cfgPath); err != nil {
		return nil, fmt.Errorf("config %s not found — run 'genestack init' first", cfgPath)
	}
	c, err := model.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	if forceLocal {
		c.Deployment.Mode = "local"
	}
	return c, nil
}

func cmdInit(cfgPath string) error {
	if _, err := os.Stat(cfgPath); err == nil {
		return fmt.Errorf("%s already exists", cfgPath)
	}
	c := model.NewCluster("lab01")
	c.Deployment.Host = "10.0.0.10"
	// A small sample so the file is immediately useful.
	c.Nodes = []model.Node{
		{Name: "ctl01", AnsibleHost: "10.0.0.51", Roles: []model.Role{model.RoleController}},
		{Name: "ctl02", AnsibleHost: "10.0.0.52", Roles: []model.Role{model.RoleController}},
		{Name: "ctl03", AnsibleHost: "10.0.0.53", Roles: []model.Role{model.RoleController}},
		{Name: "cmp01", AnsibleHost: "10.0.0.54", Roles: []model.Role{model.RoleCompute}},
	}
	if err := c.Save(cfgPath); err != nil {
		return err
	}
	fmt.Printf("wrote %s — edit deployment.host and nodes, then run 'genestack tui'\n", cfgPath)
	return nil
}

func cmdNode(cfgPath string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("node requires a subcommand: add, list, rm")
	}
	sub := args[0]
	args = args[1:]

	c, err := loadCluster(cfgPath)
	if err != nil {
		return err
	}

	switch sub {
	case "list":
		if len(c.Nodes) == 0 {
			fmt.Println("(no nodes)")
			return nil
		}
		for _, n := range c.Nodes {
			roles := make([]string, len(n.Roles))
			for i, r := range n.Roles {
				roles[i] = string(r)
			}
			fmt.Printf("%-14s %-16s %s\n", n.Name, n.AnsibleHost, strings.Join(roles, ","))
		}
		return nil

	case "add":
		// NAME is the leading positional; Go's flag package stops at the first
		// non-flag arg, so consume NAME before parsing the remaining flags.
		if len(args) < 1 || strings.HasPrefix(args[0], "-") {
			return fmt.Errorf("usage: node add NAME --ip IP --roles controller,compute")
		}
		name := args[0]
		fs := flag.NewFlagSet("node add", flag.ContinueOnError)
		ip := fs.String("ip", "", "ansible_host IP (reached over SSH, e.g. WAN)")
		nodeIP := fs.String("node-ip", "", "kubespray k8s node IP (private cluster/overlay IP)")
		rolesRaw := fs.String("roles", "controller", "comma-separated roles")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var roles []model.Role
		for _, r := range strings.Split(*rolesRaw, ",") {
			if r = strings.TrimSpace(r); r != "" {
				roles = append(roles, model.Role(r))
			}
		}
		if err := c.AddNode(model.Node{Name: name, AnsibleHost: *ip, NodeIP: *nodeIP, Roles: roles}); err != nil {
			return err
		}
		if err := c.Save(cfgPath); err != nil {
			return err
		}
		fmt.Printf("added node %s\n", name)
		return nil

	case "rm", "remove", "del":
		if len(args) < 1 {
			return fmt.Errorf("usage: node rm NAME")
		}
		if !c.RemoveNode(args[0]) {
			return fmt.Errorf("node %q not found", args[0])
		}
		if err := c.Save(cfgPath); err != nil {
			return err
		}
		fmt.Printf("removed node %s\n", args[0])
		return nil

	default:
		return fmt.Errorf("unknown node subcommand %q", sub)
	}
}

func cmdTUI(cfgPath string) error {
	c, err := loadCluster(cfgPath)
	if err != nil {
		return err
	}
	m := tui.New(c, cfgPath, statePathFor(cfgPath))
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err = p.Run()
	return err
}
