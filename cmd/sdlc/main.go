package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/tjpeel/sdlc/internal/buildinfo"
	"github.com/tjpeel/sdlc/internal/githubauth"
	"github.com/tjpeel/sdlc/internal/instructions"
	"github.com/tjpeel/sdlc/internal/project"
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
		fmt.Println("Usage: sdlc --version | runtime build [--source SDLC_DIRECTORY] | runtime status [--offline] [--github-profile NAME]")
		fmt.Println("       sdlc auth login [--provider codex|claude] | auth status [--provider codex|claude | --all]")
		fmt.Println("       sdlc auth login|status|logout --service github [--profile NAME] [status: --verify]")
		fmt.Println("       sdlc signing setup|status|verify [--profile NAME] | signing configure --file PRIVATE_PROFILE [--profile NAME]")
		fmt.Println("       sdlc instructions show | instructions set --file FILE | instructions reset")
		fmt.Println("       sdlc init (from a project repository)")
		fmt.Println("       sdlc work --reference REFERENCE (list local tickets in numeric order)")
		fmt.Println("       sdlc run --reference REFERENCE --ticket NUMBERED_FILE [--provider codex|claude] [--dry-run]")
		fmt.Println("       sdlc dashboard [--once | --json] [--run RUN_ID] [--logs]")
		fmt.Println("       sdlc interactive [--provider codex|claude]")
		fmt.Println("         Provider defaults to codex for interactive and auth commands.")
		fmt.Println("         Codex: [--approval never|on-request] (default: never)")
		fmt.Println("         Claude: [--permission-mode MODE] (default: bypassPermissions)")
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "init" {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		if err := initCommand(ctx, os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "sdlc:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "work" {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		if err := workCommand(ctx, os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "sdlc:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "run" {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		if err := runCommand(ctx, os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "sdlc:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "dashboard" {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		if err := dashboardCommand(ctx, os.Args[2:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "sdlc:", err)
			os.Exit(1)
		}
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
	if len(os.Args) >= 2 && os.Args[1] == "signing" {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		if err := signingCommand(ctx, os.Args[2:], os.Stdout); err != nil {
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
		if err := runtimeCommand(ctx, os.Args[2:], os.Stdout, os.Stderr); err != nil {
			fmt.Fprintln(os.Stderr, "sdlc:", err)
			os.Exit(1)
		}
		return
	}
	fmt.Fprintln(os.Stderr, "sdlc: unknown command; run sdlc --help")
	os.Exit(2)
}

func initCommand(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h") {
		_, err := fmt.Fprintln(output, "Usage: sdlc init\nRun from a project repository to discover local inputs and save project settings.")
		return err
	}
	if len(args) != 0 {
		return fmt.Errorf("init accepts no arguments; run it from the project repository")
	}
	directory, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("cannot locate the current directory")
	}
	result, err := project.Initialize(ctx, directory)
	if err != nil {
		return err
	}
	var summary bytes.Buffer
	fmt.Fprintf(&summary, "Project: %q\n", result.Root)
	if result.Detached {
		fmt.Fprintln(&summary, "Branch: detached HEAD")
	} else {
		fmt.Fprintf(&summary, "Branch: %q\n", result.Branch)
	}
	if result.Head == "" {
		fmt.Fprintln(&summary, "HEAD: no commits yet")
	} else {
		fmt.Fprintf(&summary, "HEAD: %s\n", result.Head)
	}
	if result.Dirty {
		fmt.Fprintln(&summary, "Source state: changes may be present before initialization.")
	} else {
		fmt.Fprintln(&summary, "Source state: no changes detected before initialization.")
	}
	for _, remote := range result.Remotes {
		fmt.Fprintf(&summary, "Remote %q: %q\n", remote.Name, remote.Identity)
	}
	for _, tool := range result.Tools {
		fmt.Fprintf(&summary, "Project file: %q\n", tool)
	}
	fmt.Fprintf(&summary, "Tickets found: %d\n", len(result.Tickets))
	for _, ticket := range result.Tickets {
		fmt.Fprintf(&summary, "  %q\n", ticket)
	}
	if result.ConfigCreated {
		fmt.Fprintf(&summary, "Project settings created: %s\n", project.ConfigPath)
	} else {
		fmt.Fprintf(&summary, "Project settings preserved: %s\n", project.ConfigPath)
	}
	fmt.Fprintf(&summary, "Checks configured: %d; review project settings before execution.\n", len(result.Config.Checks))
	fmt.Fprintln(&summary, "Private work inputs and run output are ignored and untracked.")
	for _, warning := range result.Warnings {
		fmt.Fprintf(&summary, "Warning: %s\n", warning)
	}
	fmt.Fprintln(&summary, "Local project setup complete. Use sdlc work --reference REFERENCE to list ticket order.")
	_, err = output.Write(summary.Bytes())
	return err
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
	manager := providerauth.New(runtime)
	manager.OnWait = func(provider, reason string) { queueMessage(output, provider, reason) }
	return manager.Interactive(ctx, options.provider, options.mode)
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
	service   string
	profile   string
	verify    bool
}

func parseAuthOptions(args []string) (authOptions, error) {
	var options authOptions
	if len(args) == 0 || (args[0] != "login" && args[0] != "status" && args[0] != "logout") {
		return options, fmt.Errorf("unknown authentication command; run sdlc --help")
	}
	flags := flag.NewFlagSet("auth "+args[0], flag.ContinueOnError)
	provider := flags.String("provider", defaultProvider, "codex or claude (default: codex)")
	profile := flags.String("profile", "default", "GitHub account profile")
	service := flags.String("service", "", "github: separate native GitHub CLI login")
	verify := flags.Bool("verify", false, "status only: verify GitHub login through a connected request")
	var all bool
	if args[0] == "status" {
		flags.BoolVar(&all, "all", false, "check both providers instead of the default Codex; cannot combine with --provider")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return options, err
	}
	if flags.NArg() != 0 {
		return options, fmt.Errorf("auth accepts --provider, --all for provider status, or --service github --profile NAME")
	}
	if *provider != "codex" && *provider != "claude" {
		return options, fmt.Errorf("provider must be codex or claude")
	}
	var suppliedProvider, suppliedAll, suppliedVerify, suppliedService, suppliedProfile bool
	flags.Visit(func(option *flag.Flag) {
		switch option.Name {
		case "provider":
			suppliedProvider = true
		case "all":
			suppliedAll = true
		case "verify":
			suppliedVerify = true
		case "profile":
			suppliedProfile = true
		case "service":
			suppliedService = true
		}
	})
	if suppliedService && *service == "" {
		return options, fmt.Errorf("--service must name github")
	}
	if *service != "" {
		if *service != "github" || suppliedProvider || suppliedAll || (suppliedVerify && args[0] != "status") {
			return options, fmt.Errorf("GitHub auth requires --service github without --provider or --all; --verify is for status only")
		}
		if *profile == "" {
			return options, fmt.Errorf("--profile cannot be empty")
		}
		if err := githubauth.ValidateProfile(*profile); err != nil {
			return options, err
		}
		return authOptions{action: args[0], service: "github", profile: *profile, verify: *verify}, nil
	}
	if args[0] == "logout" || suppliedVerify || suppliedProfile {
		return options, fmt.Errorf("logout, --verify and --profile require --service github")
	}
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
	if options.service == "github" {
		manager := githubauth.New(runtime)
		manager.Profile = options.profile
		return githubAuthCommand(ctx, manager, options, os.Stdout)
	}
	manager := providerauth.New(runtime)
	if options.action == "login" {
		manager.OnWait = func(provider, reason string) { queueMessage(os.Stdout, provider, reason) }
		provider := options.providers[0]
		fmt.Printf("Log in to %s using the browser instructions below. Keep this terminal private.\n", provider)
		if err := manager.Login(ctx, provider); err != nil {
			return err
		}
		fmt.Printf("%s: login completed; saved account credentials loaded successfully by a fresh container.\n", provider)
		if provider == "claude" {
			fmt.Println("On the first Claude interactive launch, complete its terminal setup; it may ask you to sign in again.")
		}
		return nil
	}
	ready := true
	manager.OnWait = func(provider, reason string) { queueMessage(os.Stdout, provider, reason) }
	for _, name := range options.providers {
		state, err := manager.Status(ctx, name)
		if err != nil {
			return err
		}
		switch state {
		case "stored":
			fmt.Printf("%s: saved account login found (offline check)\n", name)
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

func githubAuthCommand(ctx context.Context, manager githubauth.Manager, options authOptions, output io.Writer) error {
	manager.OnWait = func(provider, reason string) { queueMessage(output, provider, reason) }
	name, err := githubauth.NormalizeProfile(manager.Profile)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "GitHub profile: %s\n", name)
	switch options.action {
	case "login":
		fmt.Fprintln(output, "Log in through the official GitHub CLI browser flow. Establish your organisation SSO session first.")
		fmt.Fprintln(output, "Login is saved as plaintext in a private Docker volume. Keep this terminal private; coding and test containers do not receive this volume.")
		if err := manager.Login(ctx); err != nil {
			return err
		}
		fmt.Fprintf(output, "github: login completed; stored configuration found by a fresh container. Verify with sdlc auth status --service github --profile %s --verify.\n", name)
		return nil
	case "logout":
		if err := manager.Logout(ctx); err != nil {
			return err
		}
		fmt.Fprintln(output, "github: local Docker login removed. This does not revoke the credential at GitHub; revoke GitHub CLI authorisation in your account settings if compromised.")
		return nil
	case "status":
		state, err := manager.Status(ctx, options.verify)
		if err != nil {
			return err
		}
		switch state {
		case "stored":
			fmt.Fprintln(output, "github: saved login configuration found (offline storage check; validity and organisation access unverified)")
			return nil
		case "verified":
			session, err := manager.Acquire(ctx)
			if err != nil {
				return err
			}
			identity, err := session.Identity(ctx)
			closeErr := session.Close()
			if err != nil || closeErr != nil {
				return fmt.Errorf("cannot confirm saved GitHub account identity")
			}
			fmt.Fprintf(output, "github: verified account %s (ID %d); repository permissions and organisation SSO must be checked separately\n", identity.Login, identity.ID)
			return nil
		case "missing":
			return fmt.Errorf("GitHub login is missing; run sdlc auth login --service github --profile %s", name)
		case "invalid":
			return fmt.Errorf("GitHub configuration is unsafe or invalid; inspect private authentication storage before retrying")
		default:
			return fmt.Errorf("GitHub login verification failed; check SSO, network and account authorisation before retrying")
		}
	}
	return fmt.Errorf("unknown GitHub authentication action")
}

func queueMessage(output io.Writer, provider, reason string) {
	if reason == "runtime_busy" {
		fmt.Fprintln(output, "Waiting for the runtime build to finish.")
		return
	}
	fmt.Fprintf(output, "Waiting for another %s operation to release its account cache.\n", provider)
}
