package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/tjpeel/sdlc/internal/buildinfo"
	"github.com/tjpeel/sdlc/internal/providerauth"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Println(buildinfo.String())
		return
	}
	if len(os.Args) == 1 || (len(os.Args) == 2 && (os.Args[1] == "--help" || os.Args[1] == "help")) {
		fmt.Println("Usage: sdlc --version | runtime build [--source SDLC_DIRECTORY] | runtime status | auth login --provider codex | auth status [--provider codex]")
		return
	}
	if len(os.Args) >= 3 && os.Args[1] == "auth" {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		if err := auth(ctx, os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "sdlc:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) >= 3 && os.Args[1] == "runtime" {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		defer cancel()
		manager, err := runtimeimage.New(os.Stdout, os.Stderr)
		var state runtimeimage.State
		if err == nil {
			switch os.Args[2] {
			case "build":
				flags := flag.NewFlagSet("runtime build", flag.ContinueOnError)
				source := flags.String("source", "", "SDLC clone (uses saved source when omitted)")
				err = flags.Parse(os.Args[3:])
				if err == nil && flags.NArg() != 0 {
					err = fmt.Errorf("runtime build accepts only --source")
				}
				if err == nil {
					state, err = manager.Build(ctx, *source)
				}
			case "status":
				if len(os.Args) != 3 {
					err = fmt.Errorf("runtime status accepts no arguments")
				} else {
					state, err = manager.Status(ctx)
				}
			default:
				err = fmt.Errorf("unknown runtime command; run sdlc --help")
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "sdlc:", err)
			os.Exit(1)
		}
		fmt.Printf("Shared image: %s\nImage ID: %s\nSource revision: %s\n%s\n", runtimeimage.Image, state.ImageID, state.Revision, state.Tools)
		return
	}
	fmt.Fprintln(os.Stderr, "sdlc: unknown command; run sdlc --help")
	os.Exit(2)
}

func auth(ctx context.Context, args []string) error {
	if args[0] != "login" && args[0] != "status" {
		return fmt.Errorf("unknown authentication command; run sdlc --help")
	}
	flags := flag.NewFlagSet("auth "+args[0], flag.ContinueOnError)
	provider := flags.String("provider", "", "codex (required for login)")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("auth accepts only --provider")
	}
	if *provider == "claude" {
		return fmt.Errorf("Claude login is not implemented yet; its planned flow uses the official CLI's own cache")
	}
	if *provider != "" && *provider != "codex" {
		return fmt.Errorf("supported provider is codex")
	}
	if args[0] == "login" && *provider == "" {
		return fmt.Errorf("auth login requires --provider codex")
	}
	runtime, err := runtimeimage.New(os.Stdout, os.Stderr)
	if err != nil {
		return fmt.Errorf("cannot locate SDLC installation state")
	}
	manager := providerauth.New(runtime)
	if args[0] == "login" {
		fmt.Printf("Log in to %s using the browser instructions below. Keep this terminal private.\n", *provider)
		if err := manager.Login(ctx, *provider); err != nil {
			return err
		}
		fmt.Printf("%s: account credentials saved and loaded by a fresh container; remote validity has not been checked.\n", *provider)
		return nil
	}
	providers := providerauth.Providers
	if *provider != "" {
		providers = []string{*provider}
	}
	ready := true
	for _, name := range providers {
		state, err := manager.Status(ctx, name)
		if err != nil {
			return err
		}
		switch state {
		case "stored":
			fmt.Printf("%s: stored account login (offline check; remote validity and model access are unverified)\n", name)
		case "missing":
			fmt.Printf("%s: no stored login; run sdlc auth login --provider %s\n", name, name)
			ready = false
		case "invalid":
			fmt.Printf("%s: stored credentials could not be loaded as an account login; run sdlc auth login --provider %s\n", name, name)
			ready = false
		}
	}
	if !ready {
		return fmt.Errorf("one or more providers need an account login")
	}
	return nil
}
