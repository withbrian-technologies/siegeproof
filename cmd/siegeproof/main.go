package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/withbrian-technologies/siegeproof/internal/config"
	"github.com/withbrian-technologies/siegeproof/internal/mcp"
	"github.com/withbrian-technologies/siegeproof/internal/report"
)

var version = "0.1.0-dev"

func main() {
	jsonOutput := false
	args := os.Args[1:]
	filtered := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--json" {
			jsonOutput = true
			continue
		}
		filtered = append(filtered, arg)
	}
	args = filtered
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	var err error
	switch args[0] {
	case "version":
		err = printVersion(jsonOutput)
	case "doctor":
		err = printDoctor(jsonOutput)
	case "config":
		err = configCommand(args[1:], jsonOutput)
	case "discover":
		err = discoverCommand(args[1:], jsonOutput)
	case "report":
		err = reportCommand(args[1:], jsonOutput)
	default:
		err = fmt.Errorf("unknown command %q", args[0])
	}
	if err != nil {
		if jsonOutput {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": false, "error": err.Error()})
		} else {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		os.Exit(1)
	}
}

func printVersion(asJSON bool) error {
	value := map[string]string{"version": version, "phase": "0/1"}
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(value)
	}
	fmt.Printf("siegeproof %s (Phase 0/1 foundation)\n", version)
	return nil
}

func printDoctor(asJSON bool) error {
	value := map[string]any{
		"go_version": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH,
		"docker": commandExists("docker"), "bwrap": commandExists("bwrap"),
		"network_access": false, "targets_executed": false,
	}
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(value)
	}
	fmt.Printf("Go: %s\nOS/arch: %s/%s\nDocker: %t\nbwrap: %t\nNetwork access: no\nTargets executed: no\n",
		value["go_version"], value["os"], value["arch"], value["docker"], value["bwrap"])
	return nil
}

func configCommand(args []string, asJSON bool) error {
	if len(args) == 0 || args[0] != "validate" {
		return errors.New("usage: config validate --config PATH")
	}
	fs := flag.NewFlagSet("config validate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	path := fs.String("config", "", "path to YAML configuration")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *path == "" {
		return errors.New("--config is required")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": true, "config": cfg})
	}
	fmt.Printf("valid configuration: %s (%s transport, %s intensity)\n", *path, cfg.Target.Transport, cfg.Intensity)
	return nil
}

func discoverCommand(args []string, asJSON bool) error {
	fs := flag.NewFlagSet("discover", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	path := fs.String("config", "", "path to YAML configuration")
	reportPath := fs.String("report", "", "path to discovery report")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *path == "" {
		return errors.New("--config is required")
	}
	cfg, err := config.Load(*path)
	if err != nil {
		return err
	}
	if cfg.Target.Transport != "stdio" {
		return fmt.Errorf("discover supports only stdio transport; %s is unsupported", cfg.Target.Transport)
	}
	timeout, err := time.ParseDuration(cfg.Budgets.Timeout)
	if err != nil {
		return errors.New("invalid configured timeout")
	}
	client, err := mcp.NewClient(mcp.Config{Command: cfg.Target.Command, Timeout: timeout})
	if err != nil {
		return err
	}
	result, err := client.Discover(context.Background())
	if err != nil {
		return err
	}
	if *reportPath == "" {
		*reportPath = cfg.ReportPath
	}
	discoveryReport := report.New(result, cfg.Target.Transport, version, time.Now().UTC())
	if err := report.WriteAtomic(*reportPath, discoveryReport); err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(struct {
			OK     bool          `json:"ok"`
			Report string        `json:"report"`
			Result report.Report `json:"result"`
		}{OK: true, Report: *reportPath, Result: discoveryReport})
	}
	fmt.Printf("server %s %s · %d tools, %d resources, %d prompts\n",
		result.ServerInfo.Name, result.ServerInfo.Version,
		len(result.Tools), len(result.Resources), len(result.Prompts))
	return nil
}

func reportCommand(args []string, asJSON bool) error {
	if len(args) == 0 || args[0] != "validate" {
		return errors.New("usage: report validate --path PATH")
	}
	fs := flag.NewFlagSet("report validate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	path := fs.String("path", "", "path to discovery report")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if *path == "" {
		return errors.New("--path is required")
	}
	result, err := report.ValidateFile(*path)
	if err != nil {
		return err
	}
	if asJSON {
		return json.NewEncoder(os.Stdout).Encode(struct {
			OK     bool   `json:"ok"`
			Path   string `json:"path"`
			Schema string `json:"schema"`
		}{OK: true, Path: *path, Schema: result.Schema})
	}
	fmt.Printf("valid discovery report: %s (%s)\n", *path, result.Schema)
	return nil
}

func commandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: siegeproof [--json] version|doctor|config validate --config PATH|discover --config PATH [--report PATH]|report validate --path PATH")
}
