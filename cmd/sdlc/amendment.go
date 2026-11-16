package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/tjpeel/sdlc/internal/workrun"
)

// amendmentCommand records an immutable, keyed requirement and explicitly
// applies it to one saved PR using the existing private feedback lifecycle.
func amendmentCommand(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("amendment", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.String("run", "", "saved run")
	key := flags.String("key", "", "amendment key")
	target := flags.String("target", "", "captured specification path")
	path := flags.String("file", "", "amendment source")
	dry := flags.Bool("dry-run", false, "preview only")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(output, "Usage: sdlc amendment --run RUN_ID --key KEY --target PATH [--file FILE] [--dry-run]")
		return err
	} else if err != nil {
		return err
	}
	if flags.NArg() != 0 || *id == "" || *target == "" || !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`).MatchString(*key) {
		return errors.New("amendment requires --run, --key and --target")
	}
	action, err := resolveRunAction(ctx, *id, false, true)
	if err != nil {
		return err
	}
	found := false
	for _, in := range action.View.Journal.Plan.Inputs {
		if in.Path == *target {
			found = true
			break
		}
	}
	if !found {
		return errors.New("amendment target is not a captured run requirement")
	}
	dir := filepath.Join(action.View.Root, ".sdlc", "work", action.View.Reference, "amendments")
	if err := validateAmendmentDirectory(action.View.Root, dir); err != nil {
		return err
	}
	dest := filepath.Join(dir, *key+".md")
	tracked := adoptionGit(ctx, action.View.Root, "ls-files", "--", dest)
	trackedOutput, err := tracked.Output()
	if err != nil {
		return fmt.Errorf("cannot inspect amendment tracking: %w", err)
	}
	if len(trackedOutput) != 0 {
		return errors.New("amendment path is tracked by Git")
	}
	prefix := fmt.Sprintf("Approved specification amendment %s\nTarget captured requirement: %s\n\n", *key, *target)
	var text string
	if *path == "" {
		data, err := readHumanAnswer(dest)
		if err != nil {
			return fmt.Errorf("registered amendment unavailable: %w", err)
		}
		text = string(data)
		if !strings.HasPrefix(text, prefix) {
			return errors.New("registered amendment target does not match")
		}
	} else {
		data, err := readHumanAnswer(*path)
		if err != nil {
			return err
		}
		text = prefix + string(data)
	}
	if err := workrun.ValidateLocalPRFeedback(text); err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(text))
	if err := validateAmendmentExisting(dest, text); err != nil {
		return err
	}
	if *dry {
		fmt.Fprintf(output, "Amendment %s for %s: SHA256 %x; no files saved or work started.\n", *key, *target, sum)
		return printPRFeedbackResumePlan(output, *action.View.Journal, false)
	}
	current, err := resolveRunAction(ctx, action.View.ID, false, true)
	if err != nil {
		return err
	}
	if current.Checkpoint != action.Checkpoint {
		return errors.New("run changed while collecting amendment")
	}
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	if err := validateAmendmentDirectory(action.View.Root, dir); err != nil {
		return err
	}
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err == nil {
		_, writeErr := io.WriteString(f, text)
		if writeErr == nil {
			writeErr = f.Sync()
		}
		writeErr = errors.Join(writeErr, f.Close())
		if writeErr != nil {
			os.Remove(dest)
			return writeErr
		}
	} else if os.IsExist(err) {
		if err := validateAmendmentExisting(dest, text); err != nil {
			return err
		}
	} else {
		return err
	}
	saved, err := workrun.StoreLocalPRFeedback(action.View.Directory, text)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "Amendment %s saved: %s (SHA256 %x)\n", *key, dest, sum)
	return runCommand(ctx, append(runActionArguments(current, ""), "--pr-feedback-file", saved, "--specification-amendment"), output)
}

func validateAmendmentDirectory(root, dir string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, dir)
	if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return errors.New("unsafe amendment directory")
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, e := os.Lstat(current)
		if os.IsNotExist(e) && current == dir {
			break
		}
		if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("amendment directory must contain only real directories")
		}
	}
	cmd := adoptionGit(context.Background(), root, "check-ignore", "--quiet", "--no-index", "--", filepath.ToSlash(rel)+"/probe.md")
	if err := cmd.Run(); err != nil {
		return errors.New("amendment directory must be ignored by Git")
	}
	return nil
}
func validateAmendmentExisting(path, text string) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() != int64(len(text)) {
		return errors.New("amendment key already exists with different or unsafe content")
	}
	existing, err := os.ReadFile(path)
	if err != nil || string(existing) != text {
		return errors.New("amendment key already exists with different or unsafe content")
	}
	return nil
}
