package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/rackerlabs/genestack-cli/internal/overrides"
)

// overridesDirFor returns the local passthrough directory for a config file:
// the "overrides" directory next to cluster.yaml.
func overridesDirFor(cfgPath string) string {
	return filepath.Join(filepath.Dir(cfgPath), "overrides")
}

func cmdOverrides(cfgPath string, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("overrides requires a subcommand: list, generate, show, upload")
	}
	sub := args[0]
	args = args[1:]

	switch sub {
	case "list":
		return cmdOverridesList(cfgPath)
	case "generate", "gen":
		return cmdOverridesGenerate(cfgPath, args)
	case "show", "cat":
		return cmdOverridesShow(cfgPath, args)
	case "upload":
		return cmdOverridesUpload(cfgPath)
	default:
		return fmt.Errorf("unknown overrides subcommand %q", sub)
	}
}

func cmdOverridesList(cfgPath string) error {
	c, err := loadCluster(cfgPath)
	if err != nil {
		return err
	}
	plan, err := overrides.Plan(c, overridesDirFor(cfgPath))
	if err != nil {
		return err
	}
	fmt.Printf("passthrough dir: %s\n", overridesDirFor(cfgPath))
	for _, f := range plan {
		fmt.Printf("%-12s %s\n", f.Source, f.Rel())
	}
	return nil
}

func cmdOverridesGenerate(cfgPath string, args []string) error {
	fs := flag.NewFlagSet("overrides generate", flag.ContinueOnError)
	outDir := fs.String("output-dir", "overrides-preview", "local directory to write generated files into")
	if err := fs.Parse(args); err != nil {
		return err
	}
	c, err := loadCluster(cfgPath)
	if err != nil {
		return err
	}
	for _, f := range overrides.Managed(c) {
		dst := filepath.Join(*outDir, filepath.FromSlash(f.Rel()))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dst, f.Content, 0o644); err != nil {
			return err
		}
		fmt.Printf("wrote %s\n", dst)
	}
	fmt.Printf("\nreview these, then 'genestack overrides upload' to push them.\n")
	fmt.Printf("to customise a file beyond the modelled knobs, copy it into %s/ and edit.\n", overridesDirFor(cfgPath))
	return nil
}

func cmdOverridesShow(cfgPath string, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: overrides show <path-suffix>  (e.g. endpoints.yaml)")
	}
	c, err := loadCluster(cfgPath)
	if err != nil {
		return err
	}
	plan, err := overrides.Plan(c, overridesDirFor(cfgPath))
	if err != nil {
		return err
	}
	want := args[0]
	for _, f := range plan {
		if f.Rel() == want || strings.HasSuffix(f.Rel(), want) || f.Path == want {
			os.Stdout.Write(f.Content)
			return nil
		}
	}
	return fmt.Errorf("no planned override matching %q (see 'genestack overrides list')", want)
}

func cmdOverridesUpload(cfgPath string) error {
	c, err := loadCluster(cfgPath)
	if err != nil {
		return err
	}
	plan, err := overrides.Plan(c, overridesDirFor(cfgPath))
	if err != nil {
		return err
	}
	_, cli := buildRunner(c, overridesDirFor(cfgPath))
	ctx := context.Background()
	if err := cli.Connect(ctx); err != nil {
		return err
	}
	defer cli.Close()

	for _, f := range plan {
		if err := cli.Upload(ctx, f.Content, f.Path); err != nil {
			return fmt.Errorf("upload %s: %w", f.Path, err)
		}
		fmt.Printf("uploaded (%s) → %s\n", f.Source, f.Path)
	}
	return nil
}
