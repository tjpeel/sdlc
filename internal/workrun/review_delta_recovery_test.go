package workrun

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/runusage"
)

func unnamedDeltaOutcome() Outcome {
	out := inspectionOutcome(ReviewFileInspection{Path: "app.go", Complete: true, Evidence: "Read runtime request and failure paths"})
	out.ReviewCoverage.Scope = "delta"
	out.ReviewCoverage.Gaps = []string{}
	return out
}

func deltaEvents(t *testing.T, out Outcome, model, session string) []byte {
	t.Helper()
	init, _ := json.Marshal(map[string]any{"type": "system", "subtype": "init", "session_id": session, "model": model})
	result, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "session_id": session, "model": model, "structured_output": out})
	return append(append(append(init, '\n'), result...), '\n')
}

func TestUnnamedDeltaHandoffRetainsInspectionWithoutApproval(t *testing.T) {
	out := unnamedDeltaOutcome()
	w := &eventWriter{provider: "claude"}
	if _, err := w.Write(deltaEvents(t, out, "claude-sonnet-5-5", testNative)); err != nil {
		t.Fatal(err)
	}
	if err := w.finish("review"); err != nil {
		t.Fatal(err)
	}
	if w.result.Outcome.Status != "review_incomplete" || len(w.result.Outcome.ReviewCoverage.Inspection) != 1 {
		t.Fatal("delta was approved or inspection lost")
	}
	// Persisted reports remain strict; only native input is reconciled provisionally.
	if err := validateOutcome(out, "review"); err == nil {
		t.Fatal("unreconciled delta accepted as a stored report")
	}
	out.ReviewCoverage.Inspection[0].Path = "../unsafe"
	w = &eventWriter{provider: "claude"}
	_, _ = w.Write(deltaEvents(t, out, "claude-sonnet-5-5", testNative))
	if err := w.finish("review"); err == nil {
		t.Fatal("unsafe path bypassed validation")
	}
}

func TestUnnamedDeltaContinuesOriginalOwnerAndActualPendingFiles(t *testing.T) {
	final := inspectionOutcome(ReviewFileInspection{Path: "app.go", Complete: true, Evidence: "Previously read runtime"}, ReviewFileInspection{Path: "tests/app_test.go", Complete: true, Evidence: "Read assertions, fixtures and error cases"})
	dir, j, r, p, workspace := continuationFixture(t, unnamedDeltaOutcome(), final)
	r.Repository = inspectionRepository{fakeRepository: &fakeRepository{}, paths: []string{"app.go", "tests/app_test.go"}}
	result, err := r.independentReview(context.Background(), dir, j, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome.Status != "reviewed" || len(p.calls) != 2 || p.calls[1].ResumeID != testNative || p.calls[0].Directory != p.calls[1].Directory || !strings.Contains(p.calls[1].Prompt, "tests/app_test.go") {
		t.Fatal("delta restarted or concealed missing coverage")
	}
	if len(j.ReviewReports) != 1 || j.ReviewReports[0].Coverage.Scope == "full" {
		t.Fatal("partial inspection recorded as full")
	}
}

func TestRecoverUnnamedDeltaRequiresOriginalNativeBoundary(t *testing.T) {
	for _, fault := range []string{"", "session", "model", "boundary", "image", "snapshot", "events_link", "native_failure", "missing_native", "interruption", "tree"} {
		t.Run(fault, func(t *testing.T) {
			dir, j, r, _, _ := feedbackReadyRun(t)
			j.Attempt++
			j.State, j.ResumeState, j.StopReason = "failed", "reviewing", unnamedReviewGapError
			j.Outcome = testOutcome("implemented")
			snapshot := filepath.Join(dir, fmt.Sprintf("review-source-%d-fixture", j.Attempt))
			if err := os.Mkdir(snapshot, 0700); err != nil {
				t.Fatal(err)
			}
			native := filepath.Join(dir, fmt.Sprintf("native-review-%d", j.Attempt))
			if err := os.Mkdir(native, 0700); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			model := selectedReviewModel(j)
			a := runusage.Attempt{Version: 1, RunID: j.ID, Attempt: j.Attempt, Role: "review", Provider: model.Provider, Model: model.Name, Effort: model.Effort, ImageID: j.ImageID, StartedAt: now.Add(-time.Minute), EndedAt: now, Outcome: "failed", StopCategory: "operational", OptimizerMode: "off", SessionID: testNative, ReviewBoundary: reviewAttemptBoundary(j), Usage: runusage.Usage{NativeFinalization: &runusage.NativeFinalization{Provider: model.Provider, ExitCode: 0, ObservedAt: now}}}
			data := deltaEvents(t, unnamedDeltaOutcome(), model.Name, testNative)
			eventPath := filepath.Join(dir, fmt.Sprintf("events-%d.jsonl", j.Attempt))
			if err := os.WriteFile(eventPath, data, 0600); err != nil {
				t.Fatal(err)
			}
			switch fault {
			case "session":
				a.SessionID = "another-session"
			case "model":
				a.Model = "other-model"
			case "boundary":
				a.ReviewBoundary.Head = strings.Repeat("e", 40)
			case "image":
				a.ImageID = "sha256:other"
			case "snapshot":
				if err := os.Remove(snapshot); err != nil {
					t.Fatal(err)
				}
			case "events_link":
				if err := os.Rename(eventPath, eventPath+".real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(eventPath+".real", eventPath); err != nil {
					t.Fatal(err)
				}
			case "native_failure":
				a.Usage.NativeFinalization.ExitCode = 1
			case "missing_native":
				if err := os.Remove(native); err != nil {
					t.Fatal(err)
				}
			case "interruption":
				j.ReviewInterruption = &ReviewInterruption{}
			}
			if err := runusage.SaveAttempt(dir, a); err != nil {
				t.Fatal(err)
			}
			r.Repository = inspectionRepository{fakeRepository: &fakeRepository{}, paths: []string{"app.go", "tests/app_test.go"}}
			if fault == "tree" {
				r.Repository = &correctionRepository{fakeRepository: &fakeRepository{}, current: Revision{Head: j.Publication.HeadSHA, Tree: strings.Repeat("e", 40), Clean: true}}
			}
			previousReports := len(j.ReviewReports)
			previousAttempt := j.Attempt
			err := r.recoverUnnamedReviewGap(context.Background(), dir, j)
			if fault != "" {
				if err == nil || j.ReviewContinuation != nil || len(j.ReviewReports) != previousReports {
					t.Fatal("uncorroborated recovery changed state", fault, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			c := j.ReviewContinuation
			if c == nil || c.SessionID != testNative || c.Workspace != filepath.Base(snapshot) || c.StartAttempt != previousAttempt || j.Attempt != previousAttempt || len(j.ReviewReports) != previousReports+1 || len(c.Gaps) == 0 {
				t.Fatal("original review boundary lost")
			}
			if len(c.Inspection) != 2 || c.Inspection[1].Complete {
				t.Fatal("uninspected tests inherited without proof")
			}
			if _, err := Load(dir); err != nil {
				t.Fatal(err)
			}
			final := inspectionOutcome(ReviewFileInspection{Path: "app.go", Complete: true, Evidence: "Retained runtime inspection"}, ReviewFileInspection{Path: "tests/app_test.go", Complete: true, Evidence: "Finished test bodies and fixtures"})
			provider := &fakeProvider{outcomes: []Outcome{final}}
			r.Provider = provider
			result, err := r.independentReview(context.Background(), dir, j, snapshot)
			if err != nil || result.Outcome.Status != "reviewed" || len(provider.calls) != 1 || provider.calls[0].ResumeID != testNative || provider.calls[0].Directory != native {
				t.Fatal("saved review restarted or lost its native directory", err)
			}
		})
	}
}

func TestRepeatedUnnamedDeltaStopsWithoutAReviewRestart(t *testing.T) {
	dir, j, r, p, workspace := continuationFixture(t, unnamedDeltaOutcome(), unnamedDeltaOutcome())
	r.Repository = inspectionRepository{fakeRepository: &fakeRepository{}, paths: []string{"app.go", "tests/app_test.go"}}
	result, err := r.independentReview(context.Background(), dir, j, workspace)
	if err != nil || result.Outcome.Status != "blocked" || len(p.calls) != 2 || p.calls[1].ResumeID != testNative || j.ReviewContinuation == nil {
		t.Fatal("non-progress delta looped or restarted", err)
	}
}
