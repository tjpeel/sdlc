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
	flag.Parse()
	if *binDir == "" || flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "Usage: go run ./cmd/sdlc-install --bin-dir PATH_DIRECTORY [--source SDLC_DIRECTORY]")
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	path, err := install.Build(ctx, *source, *binDir, os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Install failed:", err)
		os.Exit(1)
	}
	fmt.Println("Installed", path)
}
