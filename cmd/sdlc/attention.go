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
	"strconv"
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
	case "awaiting_feedback", "checkpoint_checks", "checkpoint_review", "adopting", "prepared", "implementing", "repairing", "checking", "publishing", "ci", "awaiting_reviewer", "reviewing", "pr_feedback":
		return true
	}
	return false
}

func inspectRunAction(ctx context.Context, id string, feedback ...bool) (runAction, error) {
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
		if !resumableStage(stage) && !(stage == "ready" && (len(feedback) > 0 && feedback[0] || len(feedback) > 1 && feedback[1])) {
			return runAction{}, fmt.Errorf("run state %q has no supported resumable stage", journal.State)
		}
	}
	if len(feedback) > 0 && feedback[0] {
		if err := workrun.ValidatePRFeedbackResume(journal); err != nil {
			return runAction{}, err
		}
	}
	view.Journal = &journal
	return runAction{view, journal.UpdatedAt.Format(time.RFC3339Nano)}, nil
}

func resolveRunAction(ctx context.Context, id string, answer bool, feedback ...bool) (runAction, error) {
	action, err := inspectRunAction(ctx, id, feedback...)
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
	var startAt string
	var humanReviewed bool
	var ciFeedbackPath string
	var stdin bool
	var reviewRepairLimit int
	var extension int
	var feedback bool
	var acceptRecommendations bool
	var override bool
	var timeout time.Duration
	if !answer {
		flags.StringVar(&startAt, "start-at", "", "PR entry point")
		flags.BoolVar(&humanReviewed, "human-reviewed", false, "declare captured PR reviewed by a human")
		flags.StringVar(&ciFeedbackPath, "ci-feedback-file", "", "bounded supplemental CI context")
		flags.IntVar(&reviewRepairLimit, "review-repair-limit", 0, "increase the saved review allowance to at most 12")
		flags.IntVar(&extension, "extend-session-turn-budget", 0, "one-time additional native session turns (1-24)")
		flags.BoolVar(&acceptRecommendations, "accept-recommendations", false, "accept all recommendations in the latest completed review of the current PR revision")
		flags.BoolVar(&feedback, "pr-feedback", false, "assess new or changed human comments on the saved PR")
		flags.BoolVar(&override, "override-run-limits", false, "persistently lift SDLC numeric run allowances")
		flags.DurationVar(&timeout, "timeout", 0, "optional controller deadline (1m-24h; default: none)")
	}
	if answer {
		flags.StringVar(&text, "text", "", "literal answer")
		flags.StringVar(&path, "answer-file", "", "answer text file")
		flags.BoolVar(&stdin, "stdin", false, "read bounded answer from stdin")
	}
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		if answer {
			_, err = fmt.Fprintln(output, "Usage: sdlc answer --run RUN_ID [--text TEXT | --stdin | --answer-file FILE] [--dry-run]\nDefault input: . submits, /cancel cancels; .. and //cancel enter literal control lines.")
		} else {
			_, err = fmt.Fprintln(output, "Usage: sdlc resume --run RUN_ID [--accept-recommendations | --start-at awaiting-feedback|checks|review|feedback [--human-reviewed] | --override-run-limits | --pr-feedback | --extend-session-turn-budget 1..24 | --review-repair-limit 1..12 | --ci-feedback-file FILE] [--timeout DURATION] [--dry-run]")
		}
		return err
	} else if err != nil {
		return err
	}
	if *id == "" || flags.NArg() != 0 {
		return fmt.Errorf("%s requires --run RUN_ID", name)
	}
	if acceptRecommendations {
		var invalid string
		var acceptanceTimeoutSupplied bool
		flags.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "timeout":
				acceptanceTimeoutSupplied = true
			case "run", "accept-recommendations", "dry-run":
			default:
				invalid = f.Name
			}
		})
		if invalid != "" {
			return fmt.Errorf("--accept-recommendations cannot be combined with --%s", invalid)
		}
		if acceptanceTimeoutSupplied && (timeout < time.Minute || timeout > 24*time.Hour) {
			return fmt.Errorf("timeout must be between 1m and 24h")
		}
		return acceptRecommendationsCommand(ctx, *id, *dry, timeout, output)
	}
	extensionSupplied := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "extend-session-turn-budget" {
			extensionSupplied = true
		}
	})
	reviewLimitSupplied := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "review-repair-limit" {
			reviewLimitSupplied = true
		}
	})
	if reviewLimitSupplied && (reviewRepairLimit < 1 || reviewRepairLimit > 12 || feedback || override || extensionSupplied || ciFeedbackPath != "") {
		return fmt.Errorf("--review-repair-limit requires 1-12 without other recovery actions")
	}
	if extensionSupplied && (extension < 1 || extension > 24) {
		return fmt.Errorf("--extend-session-turn-budget must be 1-24")
	}
	if feedback && extensionSupplied {
		return fmt.Errorf("--pr-feedback cannot grant or extend a budget")
	}
	if override && (feedback || extensionSupplied) {
		return fmt.Errorf("--override-run-limits cannot be combined with another recovery action")
	}
	ciFeedbackSupplied := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "ci-feedback-file" {
			ciFeedbackSupplied = true
		}
	})
	if ciFeedbackSupplied && (ciFeedbackPath == "" || feedback || override || extensionSupplied) {
		return fmt.Errorf("--ci-feedback-file requires a nonempty path without another recovery action")
	}
	timeoutSupplied := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "timeout" {
			timeoutSupplied = true
		}
	})
	if !answer && timeoutSupplied && (timeout < time.Minute || timeout > 24*time.Hour) {
		return fmt.Errorf("timeout must be between 1m and 24h")
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
	if startAt != "" || humanReviewed {
		if !workrun.ValidPRStartAt(startAt) || feedback || override || extensionSupplied || reviewLimitSupplied || ciFeedbackSupplied {
			return fmt.Errorf("--start-at requires a valid PR entry point without another recovery action")
		}
	}
	action, err := resolveRunAction(ctx, *id, answer, feedback, override || startAt != "")
	if err != nil {
		return err
	}
	if startAt != "" {
		if err := workrun.ValidatePRCheckpointResume(*action.View.Journal); err != nil {
			return err
		}
	}
	if reviewLimitSupplied {
		if err := workrun.ValidateReviewRepairIncrease(*action.View.Journal, reviewRepairLimit); err != nil {
			return err
		}
	}
	if override {
		if err := workrun.ValidateRunLimitOverride(*action.View.Journal); err != nil {
			return err
		}
	}
	if ciFeedbackSupplied {
		data, err := readHumanAnswer(ciFeedbackPath)
		if err != nil {
			return err
		}
		if err := workrun.ValidateCIFeedbackResume(*action.View.Journal, string(data)); err != nil {
			return err
		}
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
			return printResumePlan(ctx, output, *action.View.Journal, "", false)
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
			return printResumePlan(ctx, output, *current.View.Journal, text, false)
		}
		path, err = saveInlineAnswer(action, text)
		if err != nil {
			return err
		}
	}
	current, err := resolveRunAction(ctx, action.View.ID, answer, feedback, override || startAt != "")
	if err != nil {
		return err
	}
	if current.Checkpoint != action.Checkpoint {
		return fmt.Errorf("run checkpoint changed; review the current questions again")
	}
	runArgs := runActionArguments(action, path)
	if startAt != "" {
		runArgs = append(runArgs, "--start-at", startAt)
		if humanReviewed {
			runArgs = append(runArgs, "--human-reviewed")
		}
	}
	if ciFeedbackSupplied {
		runArgs = append(runArgs, "--ci-feedback-file", ciFeedbackPath)
	}
	if override {
		runArgs = append(runArgs, "--override-run-limits")
	}
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "timeout" {
			runArgs = append(runArgs, "--timeout", timeout.String())
		}
	})
	if feedback {
		runArgs = append(runArgs, "--pr-feedback")
	}
	if reviewLimitSupplied {
		runArgs = append(runArgs, "--review-repair-limit", strconv.Itoa(reviewRepairLimit))
	}
	if extensionSupplied {
		runArgs = append(runArgs, "--extend-session-turn-budget", strconv.Itoa(extension))
	}
	if *dry {
		runArgs = append(runArgs, "--dry-run")
	}
	return runCommand(ctx, runArgs, output)
}

func printResumePlan(ctx context.Context, output io.Writer, journal workrun.Journal, answer string, structured bool, extension ...int) error {
	hash := ""
	if answer != "" {
		hash = fmt.Sprintf("%x", sha256.Sum256([]byte(answer)))
	}
	if !structured {
		fmt.Fprintln(output, "Offline resume plan; authentication and model access have not been checked.")
	}
	requestedTurns := 0
	if len(extension) > 0 {
		requestedTurns = extension[0]
	}
	_, deadline := ctx.Deadline()
	return encodeResumePlan(output, journal, hash, false, requestedTurns, false, &deadline)
}

func printCIFeedbackResumePlan(output io.Writer, journal workrun.Journal, text string, structured bool) error {
	if !structured {
		fmt.Fprintln(output, "Offline CI context resume plan; current candidate and failed published CI will be checked before dispatch.")
	}
	return json.NewEncoder(output).Encode(struct {
		Mode                string `json:"mode"`
		Offline             bool   `json:"offline"`
		RunID               string `json:"run_id"`
		Checkpoint          string `json:"checkpoint"`
		CIFeedbackSHA256    string `json:"ci_feedback_sha256"`
		FailedPublishedHead string `json:"failed_published_head"`
	}{"ci_feedback_resume", true, journal.ID, journal.UpdatedAt.Format(time.RFC3339Nano), fmt.Sprintf("%x", sha256.Sum256([]byte(text))), journal.Publication.HeadSHA})
}

func jsonResumePlan(output io.Writer, journal workrun.Journal, hash string, extension ...int) error {
	return jsonResumePlanAction(output, journal, hash, false, extension...)
}

func printPRFeedbackResumePlan(output io.Writer, journal workrun.Journal, structured bool) error {
	if !structured {
		fmt.Fprintln(output, "Offline PR feedback resume plan; comments, authentication and model access have not been checked.")
	}
	return jsonResumePlanAction(output, journal, "", true)
}

func jsonResumePlanAction(output io.Writer, journal workrun.Journal, hash string, feedback bool, extension ...int) error {
	requestedTurns := 0
	if len(extension) > 0 {
		requestedTurns = extension[0]
	}
	return encodeResumePlan(output, journal, hash, feedback, requestedTurns, false, nil)
}

func printRunLimitOverridePlan(output io.Writer, journal workrun.Journal, structured, requested bool, ctx context.Context, answer string, feedback bool, requestedTurns int) error {
	if !structured {
		fmt.Fprintln(output, "Offline run-limit override plan; no allowance has been changed and no work has started.")
	}
	hash := ""
	if answer != "" {
		hash = fmt.Sprintf("%x", sha256.Sum256([]byte(answer)))
	}
	_, deadline := ctx.Deadline()
	return encodeResumePlan(output, journal, hash, feedback, requestedTurns, requested, &deadline)
}

func encodeResumePlan(output io.Writer, journal workrun.Journal, hash string, feedback bool, requestedTurns int, override bool, deadline *bool) error {
	questions := journal.Outcome.Questions
	if journal.State != "waiting_for_human" {
		questions = nil
	}
	return json.NewEncoder(output).Encode(struct {
		Version               int          `json:"version"`
		Mode                  string       `json:"mode"`
		Offline               bool         `json:"offline"`
		RunID                 string       `json:"run_id"`
		Checkpoint            string       `json:"checkpoint"`
		State                 string       `json:"state"`
		Questions             []string     `json:"questions"`
		AnswerSHA256          string       `json:"answer_sha256,omitempty"`
		RequestedSessionTurns int          `json:"requested_session_turns,omitempty"`
		PRFeedback            bool         `json:"pr_feedback,omitempty"`
		OverrideRunLimits     bool         `json:"override_run_limits,omitempty"`
		RunLimitsOverridden   bool         `json:"run_limits_overridden,omitempty"`
		ExecutionHasDeadline  *bool        `json:"execution_has_deadline,omitempty"`
		Plan                  workrun.Plan `json:"plan"`
	}{1, "resume", true, journal.ID, journal.UpdatedAt.Format(time.RFC3339Nano), journal.State, questions, hash, requestedTurns, feedback, override, journal.RunLimitOverride != nil, deadline, journal.Plan})
}
