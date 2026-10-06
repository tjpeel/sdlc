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
	cliOnly := flag.Bool("cli-only", false, "install only the host command, leaving runtime unchanged")
	dependencies := flag.Bool("dependencies", false, "refresh runtime dependency pins during installation")
	dryRun := flag.Bool("dry-run", false, "preview installation offline without building or writing")
	flag.Parse()
	if *binDir == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "Usage: go run ./cmd/sdlc-install --bin-dir PATH_DIRECTORY [--source SDLC_DIRECTORY] [--cli-only | --dependencies] [--dry-run]")
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	path, err := install.BuildWithOptions(ctx, *source, *binDir, install.Options{CLIOnly: *cliOnly, Dependencies: *dependencies, DryRun: *dryRun}, os.Stdout, os.Stderr)
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
