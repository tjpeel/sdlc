package workrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/filelock"
	"github.com/tjpeel/sdlc/internal/runprogress"
	"github.com/tjpeel/sdlc/internal/runusage"
)

var ErrStopped = errors.New("run needs attention")

type Runner struct {
	Provider          Provider
	Checker           Checker
	Publisher         Publisher
	Repository        Repository
	Output            io.Writer
	ProgressOutput    io.Writer
	rawOutput         io.Writer
	progress          *runprogress.Recorder
	Instructions      string
	MaxRounds         int
	PollInterval      time.Duration
	MissingCheckGrace time.Duration
	// Reporting hooks run while the exclusive controller lock is held.
	OnStart        func(Journal) error
	OnState        func(Journal) error
	OnOutput       func([]byte)
	OnNativeOutput func([]byte)
	OnFinish       func() error
	// ReviewWorkspace creates a fresh immutable copy of the published revision.
	ReviewWorkspace func(context.Context, Journal, string) (string, error)
}

func (runner Runner) stop(directory string, journal *Journal, state, message string) error {
	if journal.State != "blocked" && journal.State != "failed" && journal.State != "waiting_for_human" {
		journal.ResumeState = journal.State
	}
	journal.State = state
	journal.StopReason = message
	if err := runner.save(directory, journal); err != nil {
		return err
	}
	if runner.Output != nil {
		fmt.Fprintf(runner.Output, "Run %s: %s. %s\n", journal.ID, state, message)
	}
	return fmt.Errorf("%w: %s", ErrStopped, message)
}

func (runner Runner) transition(directory string, journal *Journal, state string) error {
	journal.State = state
	journal.ResumeState = ""
	journal.StopReason = ""
	if runner.Output != nil {
		fmt.Fprintf(runner.Output, "Run %s: %s\n", journal.ID, state)
	}
	return runner.save(directory, journal)
}

func (runner Runner) save(directory string, journal *Journal) error {
	if err := Save(directory, journal); err != nil {
		return err
	}
	if runner.OnState != nil {
		if err := runner.OnState(*journal); err != nil {
			return err
		}
	}
	if runner.progress != nil {
		return runner.progress.Err()
	}
	return nil
}

type observedOutput struct {
	writer  io.Writer
	observe func([]byte)
}

func (output observedOutput) Write(data []byte) (int, error) {
	n, err := output.writer.Write(data)
	if n > 0 && output.observe != nil {
		output.observe(data[:n])
	}
	return n, err
}

func (runner Runner) Run(ctx context.Context, directory string, journal *Journal, answer string) (runErr error) {
	if runner.Provider == nil || runner.Checker == nil || runner.Publisher == nil || runner.Repository == nil {
		return fmt.Errorf("run controller is missing an execution boundary")
	}
	if err := realDirectory(directory); err != nil {
		return err
	}
	lockPath := filepath.Join(directory, "run.lock")
	if info, err := os.Lstat(lockPath); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("run lock must be a regular file")
	}
	lock, err := filelock.Acquire(lockPath)
	if err != nil {
		return fmt.Errorf("cannot acquire run controller lock: %w", err)
	}
	defer lock.Close()
	if !journal.UpdatedAt.IsZero() {
		current, err := Load(directory)
		if err != nil {
			return err
		}
		if current.UpdatedAt != journal.UpdatedAt {
			return fmt.Errorf("run checkpoint changed before controller ownership; retry resume to load its current state")
		}
	}
	if journal.PendingInputs != nil {
		return fmt.Errorf("input recovery is unfinished; complete it with sdlc inputs --run %s", journal.ID)
	}
	if runner.OnStart != nil {
		if err := runner.OnStart(*journal); err != nil {
			return err
		}
	}
	if runner.OnFinish != nil {
		defer func() {
			if err := runner.OnFinish(); err != nil {
				runErr = errors.Join(runErr, fmt.Errorf("cannot finish run reporting: %w", err))
			}
		}()
	}
	if runner.Output == nil {
		runner.Output = io.Discard
	}
	runner.Output = observedOutput{runner.Output, runner.OnOutput}
	runner.rawOutput = runner.Output
	recorder, err := runprogress.Open(directory, runner.ProgressOutput)
	if err != nil {
		return fmt.Errorf("cannot open run progress: %w", err)
	}
	runner.progress = recorder
	controllerOutput := runprogress.NewStream(recorder, runprogress.Event{RunID: journal.ID, Source: "sdlc"}, false)
	runner.Output = io.MultiWriter(runner.rawOutput, controllerOutput)
	defer func() { runErr = errors.Join(runErr, controllerOutput.Finish(), recorder.Close()) }()
	if err := recorder.Record(runprogress.Event{RunID: journal.ID, Source: "sdlc", Stage: journal.State, Text: "Run " + journal.ID + ": " + journal.State}); err != nil {
		return err
	}

	controllerStarted := time.Now()
	journal.Timings.Version = 1
	defer func() {
		journal.Timings.ControllerMS += time.Since(controllerStarted).Milliseconds()
		if err := runner.save(directory, journal); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}()
	if journal.State == "waiting_for_human" {
		if answer == "" {
			return runner.stop(directory, journal, "waiting_for_human", "answer the recorded questions with --answer-file before resuming")
		}
		journal.Feedback = "Recorded human questions:\n" + strings.Join(journal.Outcome.Questions, "\n") + "\nHuman answer:\n" + answer
		journal.State = journal.PendingRole
		if journal.State == "review" {
			journal.State = "reviewing"
		} else {
			journal.State = "implementing"
		}
	} else if answer != "" {
		return fmt.Errorf("this run has no pending human question")
	}
	if journal.State == "blocked" || journal.State == "failed" {
		if journal.ResumeState == "" {
			return fmt.Errorf("stopped run has no resumable stage")
		}
		journal.State = journal.ResumeState
	}
	journal.StopReason = ""
	if err := runner.save(directory, journal); err != nil {
		return err
	}
	if runner.Output == nil {
		runner.Output = io.Discard
	}
	if runner.MaxRounds == 0 {
		runner.MaxRounds = 3
	}
	if runner.PollInterval == 0 {
		runner.PollInterval = 15 * time.Second
	}
	if runner.MissingCheckGrace <= 0 {
		runner.MissingCheckGrace = 2 * time.Minute
	}
	for {
		if err := ctx.Err(); err != nil {
			return runner.stop(directory, journal, "blocked", "run was cancelled or timed out; checkpoint retained")
		}
		if err := verifyInputs(journal.Workspace, journal.Plan.Inputs); err != nil {
			return runner.stop(directory, journal, "blocked", err.Error())
		}
		switch journal.State {
		case "ready":
			checks, err := runner.Publisher.Checks(ctx, journal.Plan, journal.Publication)
			journal.CI = checks
			if err != nil || checks.Status != "passed" {
				return runner.stop(directory, journal, "blocked", "PR or CI changed after independent review")
			}
			if err := runner.transition(directory, journal, "ready"); err != nil {
				return err
			}
			fmt.Fprintln(runner.Output, "Ready for human review:", journal.Publication.URL)
			return nil
		case "prepared", "implementing", "repairing":
			status, err := runner.Provider.Status(ctx, journal.Plan.Roles.Implementation.Provider)
			if err != nil {
				return runner.stop(directory, journal, "blocked", err.Error())
			}
			if status != "stored" {
				return runner.stop(directory, journal, "blocked", "selected implementer has no usable stored login")
			}
			if journal.Attempt >= 24 {
				return runner.stop(directory, journal, "blocked", "implementation reached the configured session-turn bound")
			}
			if err := runner.transition(directory, journal, "implementing"); err != nil {
				return err
			}
			result, err := runner.session(ctx, directory, journal, "implementation", journal.Workspace, journal.SessionID)
			if result.SessionID != "" {
				if journal.SessionID != "" && result.SessionID != journal.SessionID {
					return runner.stop(directory, journal, "failed", "provider returned a different implementation session; original session retained")
				}
				journal.SessionID = result.SessionID
			}
			if result.ReportedModel != "" {
				journal.ReportedModel = result.ReportedModel
			}
			if err != nil {
				return runner.stop(directory, journal, "failed", err.Error())
			}
			journal.Outcome = result.Outcome
			journal.Feedback = ""
			switch result.Outcome.Status {
			case "waiting_for_human":
				journal.PendingRole = "implementation"
				return runner.stop(directory, journal, "waiting_for_human", strings.Join(result.Outcome.Questions, "\n"))
			case "blocked", "failed":
				return runner.stop(directory, journal, result.Outcome.Status, result.Outcome.Summary)
			case "checks_requested", "implemented":
				if result.Outcome.Status == "implemented" {
					journal.Plan.PRTitle = result.Outcome.PRTitle
					journal.Plan.PRBody = result.Outcome.PRBody
				}
				if err := runner.transition(directory, journal, "checking"); err != nil {
					return err
				}
			default:
				return runner.stop(directory, journal, "failed", "implementation returned an invalid stage")
			}
		case "checking":
			revision, err := runner.Repository.Inspect(ctx, journal.Workspace)
			if err != nil {
				return runner.stop(directory, journal, "blocked", err.Error())
			}
			if !revision.Clean {
				return runner.stop(directory, journal, "blocked", "commit the candidate changes before requesting isolated verification")
			}
			if journal.Evidence.Tree != revision.Tree || journal.Evidence.Head != revision.Head || !journal.Evidence.Passed {
				if err := verifyInputs(filepath.Join(directory, "check-inputs"), journal.Plan.CheckInputs); err != nil {
					return runner.stop(directory, journal, "blocked", err.Error())
				}
				journal.CheckAttempt++
				if err := runner.save(directory, journal); err != nil {
					return err
				}
				path := filepath.Join(directory, fmt.Sprintf("checks-%d.log", journal.CheckAttempt))
				log, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
				if err != nil {
					return err
				}
				checkStarted := time.Now()
				checkOutput := runprogress.NewStream(runner.progress, runprogress.Event{RunID: journal.ID, Source: "checks", Stage: journal.State, Attempt: journal.CheckAttempt}, false)
				checkErr := runner.Checker.Check(ctx, journal.Workspace, journal.Plan.Checks, io.MultiWriter(log, runner.rawOutput, checkOutput))
				progressErr := checkOutput.Finish()
				if progressErr != nil {
					log.Close()
					return errors.Join(checkErr, progressErr)
				}
				journal.Timings.ChecksMS += time.Since(checkStarted).Milliseconds()
				closeErr := log.Close()
				journal.Evidence = CheckEvidence{Head: revision.Head, Tree: revision.Tree, Passed: checkErr == nil, Commands: journal.Plan.Checks, Log: filepath.Base(path)}
				if closeErr != nil {
					return runner.stop(directory, journal, "blocked", "cannot retain verification evidence")
				}
				if checkErr != nil && !errors.Is(checkErr, ErrCheckFailed) {
					return runner.stop(directory, journal, "blocked", checkErr.Error())
				}
				if checkErr != nil {
					journal.Rounds++
					if journal.Rounds > runner.MaxRounds {
						return runner.stop(directory, journal, "blocked", "repository checks still fail after the repair bound")
					}
					journal.Feedback = "Configured checks failed on committed tree " + revision.Tree + ". Inspect the selected tests and code, repair within ticket scope, and request isolated checks again. Diagnostic logs remain private on the host. Ask a human if the evidence needed for repair is unavailable."
					if err := runner.transition(directory, journal, "repairing"); err != nil {
						return err
					}
					continue
				}
			}
			if journal.Outcome.Status == "checks_requested" {
				data, _ := json.Marshal(journal.Evidence)
				journal.Feedback = "Controller verification evidence: " + string(data) + ". Complete local code review and PR metadata using tjpeel-pr-draft. If the tree changes, request new checks."
				if err := runner.transition(directory, journal, "implementing"); err != nil {
					return err
				}
				continue
			}
			if revision.Head == journal.Plan.StartingSHA {
				return runner.stop(directory, journal, "blocked", "ticket produced no implementation commit")
			}
			if err := runner.transition(directory, journal, "publishing"); err != nil {
				return err
			}
		case "publishing":
			revision, err := runner.Repository.Inspect(ctx, journal.Workspace)
			if err != nil || !revision.Clean || !journal.Evidence.Passed || revision.Tree != journal.Evidence.Tree || revision.Head != journal.Evidence.Head {
				return runner.stop(directory, journal, "blocked", "source changed after verification; rerun checks before publishing")
			}
			path := filepath.Join(directory, "source.bundle")
			if info, err := os.Lstat(path); err == nil {
				if !info.Mode().IsRegular() {
					return fmt.Errorf("source checkpoint must be a regular file")
				}
				if err := os.Remove(path); err != nil {
					return err
				}
			} else if !os.IsNotExist(err) {
				return err
			}
			if err := runner.Repository.Bundle(ctx, journal.Workspace, path); err != nil {
				return runner.stop(directory, journal, "blocked", err.Error())
			}
			publication, err := runner.Publisher.Publish(ctx, journal.Plan, journal.Workspace, directory, journal.Publication, runner.Output)
			if err != nil {
				return runner.stop(directory, journal, "blocked", err.Error())
			}
			journal.Publication = publication
			if journal.Plan.Restack != nil {
				journal.RestackAudit = append(journal.RestackAudit, *journal.Plan.Restack)
				journal.Plan.Restack = nil
			}
			journal.MissingChecksSince = time.Time{}
			if err := runner.transition(directory, journal, "ci"); err != nil {
				return err
			}
		case "ci":
			checks, err := runner.Publisher.Checks(ctx, journal.Plan, journal.Publication)
			journal.CI = checks
			if err != nil {
				return runner.stop(directory, journal, "blocked", err.Error())
			}
			if checks.Status != "missing" && !journal.MissingChecksSince.IsZero() {
				journal.MissingChecksSince = time.Time{}
				if err := runner.save(directory, journal); err != nil {
					return err
				}
			}
			if err := runner.save(directory, journal); err != nil {
				return err
			}
			wait := runner.PollInterval
			if checks.Status == "missing" {
				if journal.MissingChecksSince.IsZero() {
					journal.MissingChecksSince = time.Now().UTC()
					if err := runner.save(directory, journal); err != nil {
						return err
					}
					fmt.Fprintln(runner.Output, "Waiting for CI checks to appear on the published PR.")
				}
				remaining := runner.MissingCheckGrace - time.Since(journal.MissingChecksSince)
				if remaining <= 0 {
					return runner.stop(directory, journal, "blocked", "PR has no reported CI checks after the startup grace; configure CI, then resume this run")
				}
				if remaining < wait {
					wait = remaining
				}
			}
			switch checks.Status {
			case "pending", "missing":
				waitStarted := time.Now()
				timer := time.NewTimer(wait)
				select {
				case <-ctx.Done():
					timer.Stop()
					journal.Timings.CIWaitMS += time.Since(waitStarted).Milliseconds()
					return runner.stop(directory, journal, "blocked", "CI wait cancelled; published PR retained")
				case <-timer.C:
				}
				journal.Timings.CIWaitMS += time.Since(waitStarted).Milliseconds()
				continue
			case "failed":
				journal.Rounds++
				if journal.Rounds > runner.MaxRounds {
					return runner.stop(directory, journal, "blocked", "required CI still fails after the repair bound")
				}
				journal.Feedback = "Required CI failed on the published revision. Use tjpeel-pr-monitor guidance for a bounded repair: " + checks.Details
				if err := runner.transition(directory, journal, "repairing"); err != nil {
					return err
				}
				continue
			case "passed":
				if err := runner.transition(directory, journal, "awaiting_reviewer"); err != nil {
					return err
				}
			default:
				return runner.stop(directory, journal, "blocked", "CI returned an unknown outcome")
			}
		case "awaiting_reviewer", "reviewing":
			status, err := runner.Provider.Status(ctx, journal.Plan.Roles.Review.Provider)
			if err != nil {
				return runner.stop(directory, journal, "blocked", err.Error())
			}
			if status != "stored" {
				return runner.stop(directory, journal, "awaiting_reviewer", fmt.Sprintf("authenticate %s, then resume this run for independent review", journal.Plan.Roles.Review.Provider))
			}
			checks, err := runner.Publisher.Checks(ctx, journal.Plan, journal.Publication)
			journal.CI = checks
			if err != nil {
				return runner.stop(directory, journal, "blocked", err.Error())
			}
			if checks.Status != "passed" {
				if err := runner.transition(directory, journal, "ci"); err != nil {
					return err
				}
				continue
			}
			if runner.ReviewWorkspace == nil {
				return runner.stop(directory, journal, "blocked", "published review snapshot is unavailable")
			}
			workspace, err := runner.ReviewWorkspace(ctx, *journal, directory)
			if err != nil {
				return runner.stop(directory, journal, "blocked", err.Error())
			}
			if err := runner.transition(directory, journal, "reviewing"); err != nil {
				return err
			}
			result, err := runner.session(ctx, directory, journal, "review", workspace, "")
			if err != nil {
				return runner.stop(directory, journal, "failed", err.Error())
			}
			journal.Outcome = result.Outcome
			if result.Outcome.Status == "waiting_for_human" {
				journal.PendingRole = "review"
				return runner.stop(directory, journal, "waiting_for_human", strings.Join(result.Outcome.Questions, "\n"))
			}
			if result.Outcome.Status != "reviewed" {
				return runner.stop(directory, journal, "blocked", result.Outcome.Summary)
			}
			if len(result.Outcome.Findings) > 0 {
				journal.Rounds++
				if journal.Rounds > runner.MaxRounds {
					return runner.stop(directory, journal, "blocked", "independent review still has findings after the repair bound")
				}
				data, _ := json.Marshal(result.Outcome.Findings)
				journal.Feedback = "Independent review of published head " + journal.Publication.HeadSHA + " returned actionable findings: " + string(data) + ". Repair only findings within this ticket; stop for product decisions. Repeat checks and local review."
				if err := runner.transition(directory, journal, "repairing"); err != nil {
					return err
				}
				continue
			}
			// A review is evidence for this exact PR boundary only.
			checks, err = runner.Publisher.Checks(ctx, journal.Plan, journal.Publication)
			journal.CI = checks
			if err != nil || checks.Status != "passed" {
				return runner.stop(directory, journal, "blocked", "PR or CI changed during independent review")
			}
			if err := runner.transition(directory, journal, "ready"); err != nil {
				return err
			}
			fmt.Fprintln(runner.Output, "Ready for human review:", journal.Publication.URL)
			return nil
		default:
			return fmt.Errorf("unknown recorded run stage %q", journal.State)
		}
	}
}

func verifyInputs(root string, inputs []Input) error {
	for _, input := range inputs {
		if !sourceRelative(input.Path) {
			return fmt.Errorf("run input has an unsafe path")
		}
		path := filepath.Join(root, filepath.FromSlash(input.Path))
		if err := realDirectory(filepath.Dir(path)); err != nil {
			return fmt.Errorf("selected input path changed")
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > maximumInputBytes {
			return fmt.Errorf("selected input is missing or changed")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("cannot read selected input")
		}
		hash := sha256.Sum256(data)
		if hex.EncodeToString(hash[:]) != input.SHA256 {
			return fmt.Errorf("selected input bytes changed after capture")
		}
	}
	return nil
}

func (runner Runner) session(ctx context.Context, directory string, journal *Journal, role, workspace, resume string) (result SessionResult, resultErr error) {
	// Standalone session callers retain the same raw output and progress boundaries.
	if runner.rawOutput == nil {
		runner.rawOutput = runner.Output
		if runner.rawOutput == nil {
			runner.rawOutput = io.Discard
		}
	}
	if runner.progress == nil {
		recorder, err := runprogress.Open(directory, runner.ProgressOutput)
		if err != nil {
			return result, err
		}
		runner.progress = recorder
		defer func() { resultErr = errors.Join(resultErr, recorder.Close()) }()
	}

	journal.Attempt++
	if err := runner.save(directory, journal); err != nil {
		return SessionResult{}, err
	}
	eventPath := filepath.Join(directory, fmt.Sprintf("events-%d.jsonl", journal.Attempt))
	events, err := os.OpenFile(eventPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return SessionResult{}, err
	}
	defer events.Close()
	diagnostic, err := os.OpenFile(filepath.Join(directory, fmt.Sprintf("diagnostics-%d.log", journal.Attempt)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return SessionResult{}, err
	}
	defer diagnostic.Close()
	model := journal.Plan.Roles.Implementation
	nativeDirectory := filepath.Join(directory, "native-implementation")
	if role == "review" {
		model = journal.Plan.Roles.Review
		nativeDirectory = filepath.Join(directory, fmt.Sprintf("native-review-%d", journal.Attempt))
	}
	prompt := runner.prompt(*journal, role)
	metricRole := role
	if role == "implementation" && resume != "" {
		metricRole = "repair"
	}
	attempt := runusage.Attempt{Version: 1, RunID: journal.ID, Attempt: journal.Attempt,
		Role: metricRole, Provider: model.Provider, Model: model.Name, Effort: model.Effort,
		ImageID: journal.ImageID, StartedAt: time.Now().UTC(), Outcome: "running",
		Resumed: resume != "", OptimizerMode: journal.Plan.Headroom.Mode}
	if attempt.OptimizerMode == "" {
		attempt.OptimizerMode = "off"
	}
	if err := runusage.SaveAttempt(directory, attempt); err != nil {
		return result, err
	}
	observer := runusage.NewObserver(model.Provider, model.Name)
	defer func() {
		observer.Finish()
		attempt.Usage = observer.Snapshot()
		attempt.ClientVersion = attempt.Usage.ClientVersion
		attempt.SessionID = observer.SessionID()
		attempt.HeadroomStats = result.HeadroomStats
		attempt.EndedAt = time.Now().UTC()
		attempt.Outcome = "completed"
		if resultErr != nil {
			attempt.Outcome = "failed"
		}
		if ctx.Err() != nil {
			attempt.Outcome = "aborted"
		}
		if err := runusage.SaveAttempt(directory, attempt); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("cannot retain provider usage: %w", err))
		}
	}()
	progressOutput := runprogress.NewStream(runner.progress, runprogress.Event{RunID: journal.ID, Source: "agent", Provider: model.Provider, Role: metricRole, Stage: journal.State, Attempt: journal.Attempt}, true)
	diagnosticProgress := runprogress.NewStream(runner.progress, runprogress.Event{RunID: journal.ID, Source: "agent", Provider: model.Provider, Role: metricRole, Stage: journal.State, Attempt: journal.Attempt, Text: "Diagnostic: "}, false)
	defer func() { resultErr = errors.Join(resultErr, progressOutput.Finish(), diagnosticProgress.Finish()) }()
	nativeOutput := observedOutput{io.MultiWriter(events, runner.rawOutput), runner.OnNativeOutput}
	return runner.Provider.Execute(ctx, Session{Model: model, Role: role, Workspace: workspace,
		Directory: nativeDirectory, Prompt: prompt, Schema: outcomeSchema, ResumeID: resume,
		Instructions: runner.Instructions, Headroom: journal.Plan.Headroom}, io.MultiWriter(nativeOutput, observer, progressOutput), io.MultiWriter(diagnostic, diagnosticProgress))
}

func (runner Runner) prompt(journal Journal, role string) string {
	contextDiscipline := "Keep context focused: delegate narrow investigations to the pinned subagents, request concise findings with file/line evidence, and read only relevant file ranges. Keep full logs and broad inventories on disk. Return decisions, changed paths, verification and next actions rather than entire transcripts. Rely on the native client's compaction while retaining this same implementation session; never restart to evade limits.\n"
	contextDiscipline += "For authorised supplementary repository inspection, reuse an existing selected checkout or clone beneath /workspace/.sdlc/repositories/ and exclude that directory locally with Git info/exclude. Preserve the checkout and record its revision for later inspection. Do not clone repositories into /tmp. Inspection does not authorise modifying or publishing a companion repository; follow the selected ticket delivery boundary.\n"
	if role == "implementation" && journal.SessionID != "" {
		identity, _ := json.Marshal(struct {
			Reference string `json:"reference"`
			Ticket    string `json:"ticket"`
		}{journal.Plan.Reference, journal.Plan.Ticket})
		return "Continue the original tjpeel-engineering-implement session for " + string(identity) + ". Retain the original eligibility, pinned subagent policy, ownership and whole-ticket local review gates. The controller still owns isolated checks, signing, PR publication and CI. Commit candidate changes before status checks_requested; do not run tests, dependency installation or repository scripts in this authenticated worker. Use only current controller verification evidence for an unchanged tree. Return implemented only after passing checks, complete local review and current tjpeel-pr-draft metadata; stop for human questions, missing inputs and provider access/usage/policy limits. Return the supplied JSON schema.\n" + contextDiscipline + "New controller feedback:\n" + journal.Feedback
	}
	packet, _ := json.MarshalIndent(struct {
		Plan        Plan          `json:"plan"`
		Publication Publication   `json:"publication"`
		Evidence    CheckEvidence `json:"verification"`
	}{journal.Plan, journal.Publication, journal.Evidence}, "", "  ")
	// Shared instructions are already supplied once through the native global
	// instruction file. Do not duplicate their body in the conversation.
	shared := "Follow the native global shared instructions.\n" + contextDiscipline + "Selected launch context:\n" + string(packet) + "\n\n"
	if role == "review" {
		return shared + "Use tjpeel-pr-review (invoke the skill or read its installed SKILL.md) for the actual published PR at the supplied fixed base/head. The /workspace checkout contains that exact published revision and only the selected requirement inputs. Review the complete diff and requirements. GitHub publication and required CI evidence were obtained by the controller. The verification packet records checks already run in a separate credential-free worker on the exact tested head/tree and frozen check-input hashes. Signing changes commit identity while preserving that tested tree; compare the published tree with the packet. Raw check logs and check inputs such as .env remain private to the host/check worker and are deliberately absent from this review snapshot. Their absence, not re-running checks here, and CI covering fewer commands than the separate controller checks are expected isolation and coverage boundaries, not unresolved gaps by themselves. Cite controller checks and CI separately; do not claim you ran either. If a concrete defect, missing required check or verification uncertainty remains despite the supplied results, report it as a finding, limitation or question rather than claiming approval. You have no publishing credentials. Do not modify code, post comments, publish, merge or read an implementation transcript. Use the pinned read_low/read_medium/read_high subagents where the skill directs. Wait for every required subagent to finish before returning a final handoff. Return the requested JSON schema with status reviewed only after a complete review; actionable findings need priority/file/line/failure scenario/recommendation. Return empty pr_title/pr_body. Stop on human questions, policy refusals and usage/access limits.\n" + journal.Feedback
	}
	return shared + "Use tjpeel-engineering-implement for this one selected ticket in /workspace. Invoke the skill or read its installed SKILL.md and retain its eligibility, ownership, testing, verification and whole-ticket local review gates. Use the pinned read_low/read_medium/read_high/write_medium subagent definitions as directed. The controller owns signing, push, PR creation and CI. You may make unsigned local candidate commits; do not push, create/edit a PR or merge. Do not implement unselected tickets or modify the supplied ticket/specification inputs. Repository checks and dependency installation run in a separate credential-free worker. Commit an increment before requesting those configured checks: return status checks_requested and the controller will run them and resume this exact session with evidence. Do not run dependency installation, repository scripts or tests in this authenticated worker. Evidence is valid only for the unchanged committed tree. After passing evidence, complete the required local code review and use tjpeel-pr-draft to return concise pr_title/pr_body containing the exact work reference. Return status implemented only with passing current evidence, no remaining findings/limitations/questions, committed code and local_review true. A missing specification, dependency, required tool or unresolved decision is a hard stop when encountered. Return waiting_for_human with exact questions instead of guessing. Stop on provider policy/refusal/access/usage limits. The final response must match the supplied JSON schema.\n" + journal.Feedback
}

func PrepareReview(ctx context.Context, journal Journal, directory string) (string, error) {
	id, err := NewID()
	if err != nil {
		return "", err
	}
	// A crash between snapshot creation and session start must not prevent the
	// next invocation from preparing a new independent review.
	workspace := filepath.Join(directory, fmt.Sprintf("review-source-%d-%s", journal.Attempt+1, id))
	publicationRepository := filepath.Join(directory, "publication.git")
	if journal.Plan.PublicationIdentity != nil {
		publicationRepository = filepath.Join(directory, "publisher", "publication.git")
	}
	if _, err := isolatedGit(ctx, directory, "clone", "--no-local", "--no-hardlinks", "--no-checkout", "--template=", publicationRepository, workspace); err != nil {
		return "", err
	}
	if _, err := isolatedGit(ctx, workspace, "checkout", "--detach", journal.Publication.HeadSHA); err != nil {
		return "", err
	}
	for _, input := range journal.Plan.Inputs {
		if err := copySource(journal.Workspace, workspace, input.Path, maximumInputBytes); err != nil {
			return "", err
		}
	}
	// Git respects host umask while cloning. The reviewer runs as UID 1000;
	// make the complete snapshot readable behind its private host parent.
	if err := filepath.Walk(workspace, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("review snapshot contains an unsupported file")
		}
		mode := os.FileMode(0644)
		if info.IsDir() || info.Mode()&0111 != 0 {
			mode = 0755
		}
		return os.Chmod(path, mode)
	}); err != nil {
		return "", err
	}
	return workspace, nil
}
