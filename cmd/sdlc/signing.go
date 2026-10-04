package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/tjpeel/sdlc/internal/githubauth"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/signing"
)

const signingUsage = `Usage:
  sdlc signing setup [--profile NAME] [--provider 1password] [--bootstrap-file PRIVATE_TOKEN_FILE]
  sdlc signing status [--profile NAME] [--verify] [--show-config]
  sdlc signing configure --file PRIVATE_PROFILE [--profile NAME]
  sdlc signing verify [--profile NAME]

setup guides local 1Password provisioning and saves a private signing profile.
It requires a terminal; the token is entered with echo disabled, never as an argument.
status checks local configuration and bootstrap safety without connecting to 1Password.
--verify contacts 1Password and signs a disposable local commit. It makes no GitHub or model request.
--show-config displays private vault/item references and paths, never secret contents.
The signing profile name must match the GitHub profile selected for a run.
The signing secret provider defaults to 1password, currently the only implementation.
`

type signingOptions struct {
	action, name, file, bootstrap, provider string
	verify, showConfig                      bool
}

func parseSigningOptions(args []string, output io.Writer) (signingOptions, error) {
	var options signingOptions
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(output, signingUsage)
		return options, err
	}
	switch args[0] {
	case "setup", "status", "configure", "verify":
	default:
		return options, fmt.Errorf("use sdlc signing setup, status, configure, or verify; see sdlc signing --help")
	}
	flags := flag.NewFlagSet("signing "+args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	name := flags.String("profile", "default", "matching named GitHub profile")
	file := flags.String("file", "", "private signing profile outside source repositories")
	bootstrap := flags.String("bootstrap-file", "", "existing private Service Account token file outside source repositories")
	provider := flags.String("provider", signing.DefaultProvider, "setup only: signing secret provider (1password)")
	verify := flags.Bool("verify", false, "status only: contact 1Password and verify disposable signing")
	show := flags.Bool("show-config", false, "status only: display private locator metadata without secrets")
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = io.WriteString(output, signingUsage)
			return options, err
		}
		return options, err
	}
	if flags.NArg() != 0 || *name == "" {
		return options, fmt.Errorf("select one nonempty signing profile")
	}
	if err := githubauth.ValidateProfile(*name); err != nil {
		return options, err
	}
	invalid := false
	flags.Visit(func(option *flag.Flag) {
		invalid = invalid || (option.Name == "file" && args[0] != "configure") ||
			(option.Name == "bootstrap-file" && args[0] != "setup") ||
			(option.Name == "provider" && args[0] != "setup") ||
			((option.Name == "verify" || option.Name == "show-config") && args[0] != "status")
	})
	if invalid {
		return options, fmt.Errorf("--file is for configure; --provider and --bootstrap-file are for setup; --verify and --show-config are for status")
	}
	if args[0] == "configure" && *file == "" {
		return options, fmt.Errorf("signing configure requires --file PRIVATE_PROFILE; use signing setup for guided onboarding")
	}
	if *provider == "" {
		return options, fmt.Errorf("signing secret provider must name 1password")
	}
	if err := signing.ValidateProvider(*provider); err != nil {
		return options, err
	}
	options = signingOptions{action: args[0], name: *name, file: *file, bootstrap: *bootstrap, provider: *provider, verify: *verify, showConfig: *show}
	return options, nil
}

func signingCommand(ctx context.Context, args []string, output io.Writer) error {
	options, err := parseSigningOptions(args, output)
	if err != nil || options.action == "" {
		return err
	}
	runtime, err := runtimeimage.New(output, io.Discard)
	if err != nil {
		return fmt.Errorf("cannot locate private installation state")
	}
	switch options.action {
	case "setup":
		return signingSetup(ctx, runtime.Directory, options, output, openSigningTerminal)
	case "status", "verify":
		if err := signingStatus(runtime.Directory, options.name, options.showConfig, output); err != nil {
			return err
		}
		if options.verify || options.action == "verify" {
			return verifySigning(ctx, runtime, options.name, output)
		}
		return nil
	default:
		profile, err := signing.Load(options.file)
		if err != nil {
			return err
		}
		lock, err := signing.AcquireSetup(runtime.Directory, options.name)
		if err != nil {
			return err
		}
		defer lock.Close()
		if err := signing.Store(runtime.Directory, profile, options.name); err != nil {
			return err
		}
		fmt.Fprintln(output, "Signing profile saved; public key and fingerprint match. Use signing status to check the bootstrap and signing verify to test 1Password retrieval.")
		fmt.Fprintln(output, "The profile contains private locator metadata. Its bootstrap file is a persistent plaintext bearer secret; keep both outside repositories and use host disk encryption.")
		return nil
	}
}

func signingStatus(directory, name string, showConfig bool, output io.Writer) error {
	path, err := signing.ProfilePath(directory, name)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "Signing profile %s (local check):\n", name)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		fmt.Fprintf(output, "  Configuration: missing; run sdlc signing setup --profile %s\n", name)
		return fmt.Errorf("signing setup is incomplete")
	}
	profile, err := signing.Load(path)
	if err != nil {
		fmt.Fprintln(output, "  Configuration: unsafe or invalid; inspect the private profile before using it.")
		return err
	}
	fmt.Fprintf(output, "  Configuration: saved; Ed25519 identity matches fingerprint %s\n", profile.Fingerprint)
	fmt.Fprintf(output, "  Secret provider: %s\n", profile.EffectiveProvider())
	fmt.Fprintln(output, "  Vault/key reference: configured; locator metadata hidden (use --show-config in a private terminal).")
	if showConfig {
		fmt.Fprintf(output, "  Private profile file: %q\n  Key reference: %q\n  Bootstrap file: %q\n", path, profile.Reference, profile.BootstrapFile)
	}
	if err := signing.CheckBootstrap(profile); err != nil {
		fmt.Fprintln(output, "  Service Account bootstrap: missing, unsafe or malformed; restore an owned 0600 single-line token file outside repositories.")
		return err
	}
	fmt.Fprintln(output, "  Service Account bootstrap: present; private owned 0600 file with one token line.")
	fmt.Fprintf(output, "  1Password access and signing: not checked by local status; run sdlc signing verify --profile %s\n", name)
	fmt.Fprintln(output, "  Vault permissions and GitHub Verified attribution are not established by this check.")
	return nil
}

func verifySigning(ctx context.Context, runtime runtimeimage.Manager, name string, output io.Writer) error {
	profile, err := signingProfile(runtime, name)
	if err != nil {
		return err
	}
	state, err := runtime.Status(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintln(output, "Contacting 1Password to retrieve the configured key, then testing a disposable signed commit in Docker.")
	if err := (signing.Resolver{Profile: profile}).Verify(ctx, state.ImageID); err != nil {
		return err
	}
	fmt.Fprintf(output, "Signing profile %s: key matches; disposable commit signed and verified. No GitHub or model request was made.\n", name)
	fmt.Fprintln(output, "Service Account grant scope and GitHub Verified attribution still require separate checks.")
	return nil
}

func signingProfile(runtime runtimeimage.Manager, names ...string) (signing.Profile, error) {
	name := "default"
	if len(names) == 1 {
		name = names[0]
	}
	path, err := signing.ProfilePath(runtime.Directory, name)
	if err != nil {
		return signing.Profile{}, err
	}
	return signing.Load(path)
}
