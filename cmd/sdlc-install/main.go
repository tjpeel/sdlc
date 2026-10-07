package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"

	"github.com/tjpeel/sdlc/internal/install"
)

func main() {
	source := flag.String("source", ".", "SDLC source directory")
	binDir := flag.String("bin-dir", "", "existing directory on PATH for the sdlc executable")
	cliOnly := flag.Bool("sdlc-only", false, "install only the host command, leaving runtime unchanged")
	flag.BoolVar(cliOnly, "cli-only", false, "compatibility alias for --sdlc-only")
	agentTools := flag.Bool("agent-tools", false, "update agent tools and preserve other runtime pins")
	updateDockerfile := flag.Bool("update-dockerfile", false, "with --agent-tools, write selected source Dockerfile pins")
	dependencies := flag.Bool("dependencies", false, "refresh runtime dependency pins during installation")
	dryRun := flag.Bool("dry-run", false, "preview installation offline without building or writing")
	flag.Parse()
	if *binDir == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "Usage: go run ./cmd/sdlc-install --bin-dir PATH_DIRECTORY [--source SDLC_DIRECTORY] [--sdlc-only | --agent-tools | --dependencies] [--update-dockerfile] [--dry-run]")
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	path, err := install.BuildWithOptions(ctx, *source, *binDir, install.Options{CLIOnly: *cliOnly, Dependencies: *dependencies, AgentTools: *agentTools, UpdateDockerfile: *updateDockerfile, DryRun: *dryRun}, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Install failed:", err)
		os.Exit(1)
	}
	if *dryRun {
		fmt.Println("Installation preview:", path)
	} else {
		fmt.Println("Installed", path)
	}
}
