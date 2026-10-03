package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/tjpeel/sdlc/internal/buildinfo"
	"github.com/tjpeel/sdlc/internal/instructions"
	"github.com/tjpeel/sdlc/internal/providerauth"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

const defaultProvider = "codex"

func main() {
	if len(os.Args) == 2 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Println(buildinfo.String())
		return
	}
	if len(os.Args) == 1 || (len(os.Args) == 2 && (os.Args[1] == "--help" || os.Args[1] == "help")) {
		fmt.Println("Usage: sdlc --version | runtime build [--source SDLC_DIRECTORY] | runtime status")
		fmt.Println("       sdlc auth login [--provider codex|claude] | auth status [--provider codex|claude | --all]")
		fmt.Println("       sdlc instructions show | instructions set --file FILE | instructions reset")
		fmt.Println("       sdlc interactive [--provider codex|claude]")
		fmt.Println("         Provider defaults to codex for interactive and auth commands.")
		fmt.Println("         Codex: [--approval never|on-request] (default: never)")
		fmt.Println("         Claude: [--permission-mode MODE] (default: bypassPermissions)")
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "interactive" {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		if err := interactive(ctx, os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "sdlc:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "instructions" {
		if err := instructionsCommand(os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "sdlc:", err)
			os.Exit(1)
		}
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

type interactiveOptions struct {
	provider string
	mode     string
}

func parseInteractiveOptions(args []string) (interactiveOptions, error) {
	var options interactiveOptions
	flags := flag.NewFlagSet("interactive", flag.ContinueOnError)
	provider := flags.String("provider", defaultProvider, "codex or claude (default: codex)")
	approval := flags.String("approval", "", "Codex: never or on-request (default: never)")
	permissionMode := flags.String("permission-mode", "", "Claude: default, manual, acceptEdits, plan, auto, dontAsk or bypassPermissions (default: bypassPermissions)")
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	var suppliedApproval, suppliedPermissionMode bool
	flags.Visit(func(option *flag.Flag) {
		switch option.Name {
		case "approval":
			suppliedApproval = true
		case "permission-mode":
			suppliedPermissionMode = true
		}
	})
	if flags.NArg() != 0 {
		return options, fmt.Errorf("interactive accepts --provider and its --approval or --permission-mode option")
	}
	if *provider != "codex" && *provider != "claude" {
		return options, fmt.Errorf("provider must be codex or claude")
	}
	selected := *approval
	if *provider == "codex" && suppliedPermissionMode {
		return options, fmt.Errorf("--permission-mode is only available for Claude; use --approval for Codex")
	}
	if *provider == "claude" {
		if suppliedApproval {
			return options, fmt.Errorf("--approval is only available for Codex; use --permission-mode for Claude")
		}
		selected = *permissionMode
	}
	if selected == "" && (suppliedApproval || suppliedPermissionMode) {
		return options, fmt.Errorf("an explicitly supplied approval or permission mode cannot be empty")
	}
	mode, err := providerauth.InteractiveMode(*provider, selected)
	if err != nil {
		return options, err
	}
	return interactiveOptions{provider: *provider, mode: mode}, nil
}

func interactive(ctx context.Context, args []string, output io.Writer) error {
	options, err := parseInteractiveOptions(args)
	if err != nil {
		return err
	}
	runtime, err := runtimeimage.New(os.Stdout, os.Stderr)
	if err != nil {
		return fmt.Errorf("cannot locate SDLC installation state")
	}
	fmt.Fprintln(output, "Opening an empty disposable workspace with your stored provider login and shared instructions.")
	fmt.Fprintln(output, "Use trusted prompts only. Exit through the provider CLI; workspace files and session history are discarded.")
	if options.provider == "codex" {
		fmt.Fprintf(output, "Codex access: full inside Docker; approval policy: %s.\n", options.mode)
	} else {
		fmt.Fprintf(output, "Requested Claude permission mode: %s.\n", options.mode)
	}
	return providerauth.New(runtime).Interactive(ctx, options.provider, options.mode)
}

func instructionsCommand(args []string, output io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("instructions requires show, set --file FILE, or reset")
	}
	var source string
	switch args[0] {
	case "show", "reset":
		if len(args) != 1 {
			return fmt.Errorf("instructions %s accepts no arguments", args[0])
		}
	case "set":
		flags := flag.NewFlagSet("instructions set", flag.ContinueOnError)
		file := flags.String("file", "", "Markdown file containing additional shared instructions")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if *file == "" || flags.NArg() != 0 {
			return fmt.Errorf("instructions set requires --file FILE")
		}
		source = *file
	default:
		return fmt.Errorf("unknown instructions command; run sdlc --help")
	}
	manager, err := instructions.New()
	if err != nil {
		return fmt.Errorf("cannot locate SDLC installation state")
	}
	switch args[0] {
	case "show":
		content, err := manager.Show()
		if err != nil {
			return err
		}
		_, err = output.Write(content)
		return err
	case "set":
		if err := manager.Set(source); err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, "Shared instructions updated.")
		return err
	default:
		if err := manager.Reset(); err != nil {
			return err
		}
		_, err = fmt.Fprintln(output, "Shared instructions reset to the default human-answer rule.")
		return err
	}
}

type authOptions struct {
	action    string
	providers []string
}

func parseAuthOptions(args []string) (authOptions, error) {
	var options authOptions
	if len(args) == 0 || (args[0] != "login" && args[0] != "status") {
		return options, fmt.Errorf("unknown authentication command; run sdlc --help")
	}
	flags := flag.NewFlagSet("auth "+args[0], flag.ContinueOnError)
	provider := flags.String("provider", defaultProvider, "codex or claude (default: codex)")
	var all bool
	if args[0] == "status" {
		flags.BoolVar(&all, "all", false, "check both providers instead of the default Codex; cannot combine with --provider")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return options, err
	}
	if flags.NArg() != 0 {
		return options, fmt.Errorf("auth accepts --provider, or --all for status")
	}
	if *provider != "codex" && *provider != "claude" {
		return options, fmt.Errorf("provider must be codex or claude")
	}
	var suppliedProvider, suppliedAll bool
	flags.Visit(func(option *flag.Flag) {
		switch option.Name {
		case "provider":
			suppliedProvider = true
		case "all":
			suppliedAll = true
		}
	})
	if suppliedAll && suppliedProvider {
		return options, fmt.Errorf("cannot combine --all with --provider")
	}
	providers := []string{*provider}
	if all {
		providers = providerauth.Providers
	}
	return authOptions{action: args[0], providers: providers}, nil
}

func auth(ctx context.Context, args []string) error {
	options, err := parseAuthOptions(args)
	if err != nil {
		return err
	}
	runtime, err := runtimeimage.New(os.Stdout, os.Stderr)
	if err != nil {
		return fmt.Errorf("cannot locate SDLC installation state")
	}
	manager := providerauth.New(runtime)
	if options.action == "login" {
		provider := options.providers[0]
		fmt.Printf("Log in to %s using the browser instructions below. Keep this terminal private.\n", provider)
		if err := manager.Login(ctx, provider); err != nil {
			return err
		}
		fmt.Printf("%s: account credentials saved and loaded by a fresh container; remote validity has not been checked.\n", provider)
		return nil
	}
	ready := true
	for _, name := range options.providers {
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
