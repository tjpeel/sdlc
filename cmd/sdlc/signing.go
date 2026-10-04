package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/tjpeel/sdlc/internal/githubauth"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/signing"
)

func signingCommand(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 0 || (args[0] != "configure" && args[0] != "verify") {
		return fmt.Errorf("use sdlc signing configure --file PRIVATE_PROFILE or signing verify [--profile NAME]")
	}
	flags := flag.NewFlagSet("signing configure", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	name := flags.String("profile", "default", "named GitHub profile; default preserves existing storage")
	file := flags.String("file", "", "private signing profile outside source repositories")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *name == "" {
		return fmt.Errorf("select one nonempty signing profile")
	}
	if err := githubauth.ValidateProfile(*name); err != nil {
		return err
	}
	if args[0] == "configure" && *file == "" {
		return fmt.Errorf("signing configure requires --file PRIVATE_PROFILE")
	}
	if args[0] == "verify" && *file != "" {
		return fmt.Errorf("signing verify uses the saved profile; --file is for configure")
	}
	runtime, err := runtimeimage.New(output, io.Discard)
	if err != nil {
		return fmt.Errorf("cannot locate private installation state")
	}
	if args[0] == "verify" {
		profile, err := signingProfile(runtime, *name)
		if err != nil {
			return err
		}
		state, err := runtime.Status(ctx)
		if err != nil {
			return err
		}
		if err := (signing.Resolver{Profile: profile}).Verify(ctx, state.ImageID); err != nil {
			return err
		}
		fmt.Fprintf(output, "Signing profile %s: key matches; disposable commit signed and verified. No GitHub or model request was made.\n", *name)
		return nil
	}
	profile, err := signing.Load(*file)
	if err != nil {
		return err
	}
	if err := signing.Store(runtime.Directory, profile, *name); err != nil {
		return err
	}
	fmt.Fprintln(output, "Signing profile saved; public key and fingerprint match. Private-key retrieval is checked before publication.")
	fmt.Fprintln(output, "The bootstrap file is a persistent bearer secret. Keep it outside repositories, restrict it to your user, and use host disk encryption. OS credential-store integration remains future work.")
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
