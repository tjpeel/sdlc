package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/tjpeel/sdlc/internal/dashboard"
	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
	"github.com/tjpeel/sdlc/internal/workrun"
)

type runAction struct {
	View       runstatus.View
	Checkpoint string
}

func resumableStage(state string) bool {
	switch state {
	case "prepared", "implementing", "repairing", "checking", "publishing", "ci", "awaiting_reviewer", "reviewing":
		return true
	}
	return false
}

func inspectRunAction(ctx context.Context, id string) (runAction, error) {
	if err := ctx.Err(); err != nil {
		return runAction{}, err
	}
	runtime, err := runtimeimage.New(io.Discard, io.Discard)
	if err != nil {
		return runAction{}, err
	}
	views, err := runstatus.New(runtime.Directory).List(time.Now().UTC())
	if err != nil {
		return runAction{}, err
	}
	view, err := dashboard.Select(views, id)
	if err != nil {
		return runAction{}, err
	}
	if !view.Available || view.Preparation || view.Journal == nil {
		return runAction{}, fmt.Errorf("run checkpoint is unavailable or still in preparation")
	}
	directory, err := workrun.RunDirectory(view.Root, view.Reference, filepath.Base(view.Ticket), view.ID, false)
	if err != nil || directory != view.Directory {
		return runAction{}, fmt.Errorf("recorded run directory does not match its identity")
	}
	lock, err := probeRunController(directory)
	if err != nil {
		return runAction{}, err
	}
	if lock != nil {
		defer lock.Close()
	}
	journal, err := workrun.Load(directory)
	if err != nil {
		return runAction{}, err
	}
	if journal.ID != view.ID || journal.Plan.Root != view.Root || journal.Plan.Reference != view.Reference || journal.Plan.Ticket != view.Ticket || !journal.UpdatedAt.Equal(view.UpdatedAt) {
		return runAction{}, fmt.Errorf("run checkpoint changed; select the run again")
	}
	if view.Live || !(view.Stopped || view.Stale) {
		return runAction{}, fmt.Errorf("run controller is still active")
	}
	stage := journal.State
	if stage == "waiting_for_human" {
		if len(journal.Outcome.Questions) == 0 || (journal.PendingRole != "implementation" && journal.PendingRole != "review") {
			return runAction{}, fmt.Errorf("run has no supported pending question")
		}
	} else {
		if stage == "blocked" || stage == "failed" {
			stage = journal.ResumeState
		}
		if !resumableStage(stage) {
			return runAction{}, fmt.Errorf("run state %q has no supported resumable stage", journal.State)
		}
	}
	view.Journal = &journal
	return runAction{view, journal.UpdatedAt.Format(time.RFC3339Nano)}, nil
}

func resolveRunAction(ctx context.Context, id string, answer bool) (runAction, error) {
	action, err := inspectRunAction(ctx, id)
	if err != nil {
		return runAction{}, err
	}
	if action.View.Journal.PendingInputs != nil {
		return runAction{}, fmt.Errorf("input recovery is unfinished; complete it with sdlc inputs --run %s", action.View.ID)
	}
	if answer && action.View.State != "waiting_for_human" {
		return runAction{}, fmt.Errorf("this run has no pending human question")
	}
	if !answer && action.View.State == "waiting_for_human" {
		return runAction{}, fmt.Errorf("answer the recorded questions with sdlc answer --run %s", action.View.ID)
	}
	return action, nil
}

func probeRunController(directory string) (*os.File, error) {
	path := filepath.Join(directory, "run.lock")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return nil, fmt.Errorf("run controller lock is unsafe")
	}
	lock, err := filelock.Acquire(path)
	if err != nil {
		if errors.Is(err, filelock.ErrBusy) {
			return nil, fmt.Errorf("another controller owns this run: %w", err)
		}
		return nil, fmt.Errorf("cannot probe run controller: %w", err)
	}
	opened, statErr := lock.Stat()
	current, currentErr := os.Lstat(path)
	if statErr != nil || currentErr != nil || !os.SameFile(info, opened) || !os.SameFile(info, current) {
		lock.Close()
		return nil, fmt.Errorf("run controller lock changed while opening")
	}
	return lock, nil
}

func runActionArguments(action runAction, answerPath string) []string {
	args := []string{"--reference", action.View.Reference, "--ticket", filepath.Base(action.View.Ticket), "--resume", action.View.ID, "--run-root", action.View.Root, "--checkpoint", action.Checkpoint}
	if answerPath != "" {
		args = append(args, "--answer-file", answerPath)
	}
	return args
}

func validInlineAnswer(text string) error {
	if len(text) > 64*1024 || !utf8.ValidString(text) || strings.TrimSpace(text) == "" {
		return fmt.Errorf("answer must be nonempty UTF-8 text of at most 64 KiB")
	}
	return nil
}

func saveInlineAnswer(action runAction, text string) (string, error) {
	if err := validInlineAnswer(text); err != nil {
		return "", err
	}
	current, err := resolveRunAction(context.Background(), action.View.ID, true)
	if err != nil {
		return "", err
	}
	if current.Checkpoint != action.Checkpoint || current.View.Directory != action.View.Directory {
		return "", fmt.Errorf("run checkpoint changed; review the current questions again")
	}
	directory := filepath.Join(action.View.Directory, "answers")
	if err := os.Mkdir(directory, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0700 {
		return "", fmt.Errorf("answer history must be a private real directory")
	}
	// Unique files are never replaced: an asynchronously dispatched controller
	// can read its answer after a subsequent answer has been prepared.
	file, err := os.CreateTemp(directory, "answer-*.txt")
	if err != nil {
		return "", err
	}
	path := file.Name()
	if _, err = io.WriteString(file, text); err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		os.Remove(path)
		return "", err
	}
	return path, nil
}

var errAnswerCancelled = errors.New("answer cancelled")

func readTerminalAnswer(input io.Reader) (string, error) {
	scanner := bufio.NewScanner(io.LimitReader(input, 64*1024+1024))
	scanner.Buffer(make([]byte, 1024), 64*1024+1)
	var text strings.Builder
	for scanner.Scan() {
		line := scanner.Text()
		if line == "/cancel" {
			return "", errAnswerCancelled
		}
		if line == "." {
			value := text.String()
			return value, validInlineAnswer(value)
		}
		if line == ".." {
			line = "."
		}
		if line == "//cancel" {
			line = "/cancel"
		}
		if text.Len()+len(line)+1 > 64*1024 {
			return "", fmt.Errorf("answer exceeds 64 KiB")
		}
		text.WriteString(line)
		text.WriteByte('\n')
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("cannot read bounded answer: %w", err)
	}
	return "", fmt.Errorf("answer was not submitted; use a line containing . to submit")
}

func answerCommand(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	return attentionCommand(ctx, args, input, output, true)
}
func resumeCommand(ctx context.Context, args []string, output io.Writer) error {
	return attentionCommand(ctx, args, nil, output, false)
}
func attentionCommand(ctx context.Context, args []string, input io.Reader, output io.Writer, answer bool) error {
	name := "resume"
	if answer {
		name = "answer"
	}
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	id := flags.String("run", "", "run ID or unique prefix")
	dry := flags.Bool("dry-run", false, "offline preview")
	var text, path string
	var stdin bool
	if answer {
		flags.StringVar(&text, "text", "", "literal answer")
		flags.StringVar(&path, "answer-file", "", "answer text file")
		flags.BoolVar(&stdin, "stdin", false, "read bounded answer from stdin")
	}
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		if answer {
			_, err = fmt.Fprintln(output, "Usage: sdlc answer --run RUN_ID [--text TEXT | --stdin | --answer-file FILE] [--dry-run]\nDefault input: . submits, /cancel cancels; .. and //cancel enter literal control lines.")
		} else {
			_, err = fmt.Fprintln(output, "Usage: sdlc resume --run RUN_ID [--dry-run]")
		}
		return err
	} else if err != nil {
		return err
	}
	if *id == "" || flags.NArg() != 0 {
		return fmt.Errorf("%s requires --run RUN_ID", name)
	}
	sources := 0
	suppliedText := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "text" {
			suppliedText = true
			sources++
		}
		if f.Name == "answer-file" {
			sources++
		}
		if f.Name == "stdin" && stdin {
			sources++
		}
	})
	if sources > 1 {
		return fmt.Errorf("select only one of --text, --stdin or --answer-file")
	}
	if sources == 1 && !suppliedText && !stdin && path == "" {
		return fmt.Errorf("--answer-file cannot be empty")
	}
	action, err := resolveRunAction(ctx, *id, answer)
	if err != nil {
		return err
	}
	if answer {
		fmt.Fprintf(output, "Run %s | project %s\nTicket: %s/%s\nRecorded questions:\n", action.View.ID, dashboard.SafeText(action.View.Root), dashboard.SafeText(action.View.Reference), dashboard.SafeText(filepath.Base(action.View.Ticket)))
		if workrun.CanAttachInputs(*action.View.Journal) == nil {
			fmt.Fprintf(output, "Missing file? Inspect recorded inputs with sdlc inputs --run %s\n", action.View.ID)
		}
		for _, question := range action.View.Questions {
			fmt.Fprintln(output, dashboard.SafeText(question))
		}
		if *dry && sources == 0 {
			return printResumePlan(output, *action.View.Journal, "", false)
		}
		if path != "" {
			data, err := readHumanAnswer(path)
			if err != nil {
				return err
			}
			text = string(data)
		} else if stdin {
			data, err := io.ReadAll(io.LimitReader(input, 64*1024+1))
			if err != nil {
				return err
			}
			text = string(data)
		} else if !suppliedText {
			fmt.Fprintln(output, "Enter answer; . submits, /cancel cancels. Use .. or //cancel for literal control lines.")
			text, err = readTerminalAnswer(input)
			if errors.Is(err, errAnswerCancelled) {
				_, err = fmt.Fprintln(output, "Answer cancelled.")
				return err
			}
			if err != nil {
				return err
			}
		}
		if err := validInlineAnswer(text); err != nil {
			return err
		}
		if *dry {
			current, err := resolveRunAction(ctx, action.View.ID, true)
			if err != nil {
				return err
			}
			if current.Checkpoint != action.Checkpoint {
				return fmt.Errorf("run checkpoint changed; review the current questions again")
			}
			return printResumePlan(output, *current.View.Journal, text, false)
		}
		path, err = saveInlineAnswer(action, text)
		if err != nil {
			return err
		}
	}
	current, err := resolveRunAction(ctx, action.View.ID, answer)
	if err != nil {
		return err
	}
	if current.Checkpoint != action.Checkpoint {
		return fmt.Errorf("run checkpoint changed; review the current questions again")
	}
	runArgs := runActionArguments(action, path)
	if *dry {
		runArgs = append(runArgs, "--dry-run")
	}
	return runCommand(ctx, runArgs, output)
}

func printResumePlan(output io.Writer, journal workrun.Journal, answer string, structured bool) error {
	hash := ""
	if answer != "" {
		hash = fmt.Sprintf("%x", sha256.Sum256([]byte(answer)))
	}
	if !structured {
		fmt.Fprintln(output, "Offline resume plan; authentication and model access have not been checked.")
	}
	return jsonResumePlan(output, journal, hash)
}

func jsonResumePlan(output io.Writer, journal workrun.Journal, hash string) error {
	questions := journal.Outcome.Questions
	if journal.State != "waiting_for_human" {
		questions = nil
	}
	return json.NewEncoder(output).Encode(struct {
		Version      int          `json:"version"`
		Mode         string       `json:"mode"`
		Offline      bool         `json:"offline"`
		RunID        string       `json:"run_id"`
		Checkpoint   string       `json:"checkpoint"`
		State        string       `json:"state"`
		Questions    []string     `json:"questions"`
		AnswerSHA256 string       `json:"answer_sha256,omitempty"`
		Plan         workrun.Plan `json:"plan"`
	}{1, "resume", true, journal.ID, journal.UpdatedAt.Format(time.RFC3339Nano), journal.State, questions, hash, journal.Plan})
}
