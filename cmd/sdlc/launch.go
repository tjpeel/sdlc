package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/terminallaunch"
)

type launchObserverKey struct{}
type launchObserver func(terminallaunch.RunIdentity) error

func observeLaunch(ctx context.Context, identity terminallaunch.RunIdentity) error {
	if observe, ok := ctx.Value(launchObserverKey{}).(launchObserver); ok {
		return observe(identity)
	}
	return nil
}

func launchStore(state string) (*terminallaunch.Store, error) {
	if state == "" {
		manager, err := runtimeimage.New(io.Discard, io.Discard)
		if err != nil {
			return nil, err
		}
		state = manager.Directory
	}
	state, err := filepath.Abs(state)
	if err != nil {
		return nil, err
	}
	return terminallaunch.NewStore(filepath.Join(state, "launches")), nil
}

// Adapter options are removed without interpreting values as flags. This keeps
// repeatable inputs, literal punctuation and explicitly supplied frozen options.
var runValueFlags = map[string]bool{"reference": true, "ticket": true, "provider": true, "github-profile": true, "input": true, "base": true, "branch": true, "repo": true, "model": true, "effort": true, "review-model": true, "review-effort": true, "resume": true, "answer-file": true, "timeout": true, "notify": true, "parallel": true, "headroom": true}

func controllerArgs(args []string) []string {
	return withoutRunFlags(args, "json", "terminal", "launch-id")
}

func withoutRunFlags(args []string, omit ...string) []string {
	omitted := map[string]bool{}
	for _, name := range omit {
		omitted[name] = true
	}
	var result []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, _, assigned := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if strings.HasPrefix(arg, "-") && omitted[name] {
			if (runValueFlags[name] || name == "terminal" || name == "launch-id") && !assigned {
				i++
			}
			continue
		}
		result = append(result, arg)
		if strings.HasPrefix(arg, "-") && runValueFlags[name] && !assigned && i+1 < len(args) {
			i++
			result = append(result, args[i])
		}
	}
	return result
}

func offlinePlan(ctx context.Context, args []string, root string) ([]byte, error) {
	options, err := parseRunOptions(controllerArgs(args))
	if err != nil {
		return nil, err
	}
	options.dryRun, options.jsonOutput, options.terminal = true, true, true
	options.root = root
	var output bytes.Buffer
	if options.all {
		err = runSeriesCommand(ctx, options, &output)
	} else {
		err = runSelectedCommand(ctx, options, &output)
	}
	return output.Bytes(), err
}

func planHash(plan []byte) string {
	digest := sha256.Sum256(plan)
	return hex.EncodeToString(digest[:])
}

func launchRunCommand(ctx context.Context, args []string, options runOptions, output io.Writer) error {
	root := options.root
	if root == "" {
		var err error
		root, err = os.Getwd()
		if err != nil {
			return err
		}
	}
	root, err := checkoutRoot(ctx, root)
	if err != nil {
		return err
	}
	plan, err := offlinePlan(ctx, args, root)
	if err != nil {
		return err
	}
	return launchPreparedRun(ctx, root, controllerArgs(args), planHash(plan), options.launchID, options.jsonOutput, output, terminallaunch.ITerm2{})
}

func launchPreparedRun(ctx context.Context, root string, args []string, previewHash, id string, structured bool, output io.Writer, backend terminallaunch.Backend) error {
	if id == "" {
		var err error
		id, err = terminallaunch.NewID()
		if err != nil {
			return err
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return err
	}
	store, err := launchStore("")
	if err != nil {
		return err
	}
	receipt, launchErr := store.Launch(ctx, terminallaunch.Request{ID: id, Root: root, Executable: executable, Args: append([]string{"run"}, args...), PreviewHash: previewHash}, backend)
	if receipt.ID != "" {
		if structured {
			err = json.NewEncoder(output).Encode(receipt)
		} else {
			fmt.Fprintf(output, "Launch %s: %s\n", receipt.ID, receipt.State)
			if launchErr == nil {
				fmt.Fprintln(output, "Requested an independent iTerm2 tab with select=false. Use launch status to check controller startup.")
			}
			if launchErr != nil {
				fmt.Fprintln(output, "Open a separate terminal manually:", receipt.ManualCommand)
			}
		}
	}
	return errors.Join(launchErr, err)
}

func launchCommand(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 0 || (args[0] != "status" && args[0] != "execute") {
		return fmt.Errorf("usage: sdlc launch status --id UUID [--json]; internal handoff: launch execute --id UUID [--state-dir PRIVATE_DIRECTORY]")
	}
	flags := flag.NewFlagSet("launch "+args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.String("id", "", "launch UUID")
	state := flags.String("state-dir", "", "private SDLC state for this launch")
	structured := flags.Bool("json", false, "one launch receipt")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *id == "" || flags.NArg() != 0 || args[0] == "execute" && *structured {
		return fmt.Errorf("invalid launch arguments")
	}
	store, err := launchStore(*state)
	if err != nil {
		return err
	}
	if args[0] == "status" {
		receipt, err := store.Status(*id)
		if err != nil {
			return err
		}
		if *structured {
			return json.NewEncoder(output).Encode(receipt)
		}
		fmt.Fprintf(output, "Launch %s: %s\nProject: %s\nReference: %s\nRun IDs: %s\n", receipt.ID, receipt.State, receipt.Root, receipt.Reference, strings.Join(receipt.RunIDs, ", "))
		if receipt.Error != "" {
			fmt.Fprintln(output, receipt.Error)
		}
		return nil
	}
	request, err := store.Consume(*id)
	if err != nil {
		return err
	}
	// This is the dedicated terminal helper process, never the shell process.
	if err = os.Chdir(request.Root); err == nil {
		err = os.Setenv("SDLC_STATE_DIR", filepath.Dir(store.Directory))
	}
	if err == nil && request.PreviewHash != "" {
		var plan []byte
		plan, err = offlinePlan(ctx, request.Args[1:], request.Root)
		if err == nil && planHash(plan) != request.PreviewHash {
			err = fmt.Errorf("launch plan changed; return to the shell and review again")
		}
	}
	if err == nil {
		ctx = context.WithValue(ctx, launchObserverKey{}, launchObserver(func(identity terminallaunch.RunIdentity) error { return store.RecordRun(*id, identity) }))
		err = runCommand(ctx, request.Args[1:], output)
	}
	return errors.Join(err, store.Complete(*id, err))
}
