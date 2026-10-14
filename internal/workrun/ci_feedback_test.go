package workrun

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type ciFeedbackStatusMutation struct {
	*fakeProvider
	mutate func()
}

func (p ciFeedbackStatusMutation) Status(ctx context.Context, name string) (string, error) {
	p.mutate()
	return p.fakeProvider.Status(ctx, name)
}

const supplementalContext = "The failed startup check identifies ExampleOptions.RequiredValue as missing at src/Startup.cs:42. Literal data: $(touch example), `echo example`."

func TestCIFeedbackAdmitsMissingFailureEvidenceWithoutDiagnosticParser(t *testing.T) {
	_, j, _, _, _ := ciFeedbackRun(t)
	j.Outcome.Summary = "Failed published CI supplies no failing-step evidence. The controller must provide bounded failure details before a repair can proceed."
	j.StopReason = j.Outcome.Summary
	if err := ValidateCIFeedbackResume(*j, supplementalContext); err != nil {
		t.Fatal(err)
	}
}

func TestOrdinaryResumeAutomaticallyRefreshesCIContextInOriginalSession(t *testing.T) {
	dir, j, r, p, pub := ciFeedbackRun(t)
	r.CIFeedback = ""
	j.CIRepairBudget.Used = 3
	j.Rounds = 27
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	failureContext := &CIFailureContext{Head: testHead, Checks: []CIFailedCheck{{Kind: "check", Name: "offline unit suite", CheckID: 41, AppID: 90, AppSlug: "example-checks", Conclusion: "failure", Text: "UnknownProbeFailure: expected one item at src/Example.cs:42."}}}
	pub.ciResults = []CIResult{{Status: "failed", FailureContext: failureContext}}
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if len(p.calls) != 1 || p.calls[0].ResumeID != testNative || !strings.Contains(p.calls[0].Prompt, "UnknownProbeFailure") || strings.Count(p.calls[0].Prompt, "UnknownProbeFailure") != 1 || j.CIRepairBudget.Used != 3 || j.CIRepairBudget.StartRound != 24 || j.CIRepairBudget.LastGrantedHead != testHead || j.Rounds != 27 || j.Attempt != 13 || len(j.CIFeedbackRecoveries) != 1 || !j.CIFeedbackRecoveries[0].Automatic || !j.CIFeedbackRecoveries[0].Completed {
		t.Fatal("ordinary resume changed grant or did not dispatch private evidence once")
	}
	beforeCI := pub.ci
	if err := r.Run(context.Background(), dir, j, ""); err == nil || len(p.calls) != 1 || pub.ci != beforeCI {
		t.Fatal("same failed head automatically dispatched context twice", err)
	}
	if strings.Contains(r.prompt(*j, "review"), "UnknownProbeFailure") {
		t.Fatal("private CI context reached independent review")
	}
}

func TestOrdinaryResumeHoldsUnavailableOrChangedAutomaticCIContext(t *testing.T) {
	for _, kind := range []string{"none", "empty", "transport", "stale head", "context changed", "attempt changed"} {
		t.Run(kind, func(t *testing.T) {
			dir, j, r, p, pub := ciFeedbackRun(t)
			r.CIFeedback = ""
			ctx := &CIFailureContext{Head: testHead, Checks: []CIFailedCheck{{Kind: "check", Name: "offline suite", CheckID: 41, AppID: 90, AppSlug: "example-checks", Conclusion: "failure", Text: "UnknownProbeFailure evidence."}}}
			pub.ciResults = []CIResult{{Status: "failed", FailureContext: ctx}}
			if kind == "attempt changed" {
				ctx.Checks[0].Kind = "actions"
				ctx.Checks[0].AppID = 15368
				ctx.Checks[0].AppSlug = "github-actions"
				ctx.Checks[0].RunID = 88
				ctx.Checks[0].RunAttempt = 2
				ctx.Checks[0].JobID = 99
				ctx.Checks[0].RepositoryID = 7
			}
			switch kind {
			case "none":
				pub.ciResults[0].FailureContext = nil
			case "empty":
				ctx.Checks[0].Text = ""
			case "transport":
				pub.ciErr = errors.New("offline API unavailable")
			case "stale head":
				ctx.Head = testBase
			case "context changed":
				if err := r.prepareCIFeedback(context.Background(), dir, j); err != nil {
					t.Fatal(err)
				}
				ctx.Checks[0].Text = "UnknownProbeFailure changed evidence."
			case "attempt changed":
				if err := r.prepareCIFeedback(context.Background(), dir, j); err != nil {
					t.Fatal(err)
				}
				ctx.Checks[0].RunAttempt = 3
			}
			if err := r.Run(context.Background(), dir, j, ""); err == nil || len(p.calls) != 0 || j.CIRepairBudget.Used != 2 || j.Attempt != 12 || j.Rounds != 26 {
				t.Fatal("unknown or changed automatic CI context dispatched", err)
			}
		})
	}
}

func TestCIFeedbackLimitationsEvidenceSurvivesPrepareSaveLoadAndDispatch(t *testing.T) {
	dir, j, r, p, _ := ciFeedbackRun(t)
	j.Outcome.Summary = "Published CI failed without diagnostic details."
	j.Outcome.Limitations = []string{"Missing failure context."}
	j.StopReason = j.Outcome.Summary
	p.outcomes = []Outcome{j.Outcome}
	if err := ValidateCIFeedbackResume(*j, supplementalContext); err != nil {
		t.Fatal("combined native evidence was not admitted", err)
	}
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	if err := r.prepareCIFeedback(context.Background(), dir, j); err != nil {
		t.Fatal("admitted native evidence could not be persisted", err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	r.CIFeedback = ""
	if err := r.Run(context.Background(), dir, &loaded, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if len(p.calls) != 1 || p.calls[0].ResumeID != testNative || !strings.Contains(p.calls[0].Prompt, "ExampleOptions.RequiredValue") || !loaded.CIFeedbackRecoveries[0].Completed || loaded.CIFeedbackRecoveries[0].DispatchAttempt != 13 || loaded.CIFeedbackRecoveries[0].Text != "" || loaded.CIRepairBudget.Used != 2 || loaded.Rounds != 26 {
		t.Fatal("combined evidence did not reach original session without a new grant")
	}
}

func TestCIFeedbackFrozenLimitationsRefuseDriftAndOtherHolds(t *testing.T) {
	for _, kind := range []string{"changed eligible context", "removed context", "auth", "access", "requirements"} {
		t.Run(kind, func(t *testing.T) {
			dir, j, r, p, pub := ciFeedbackRun(t)
			j.Outcome.Limitations = []string{"Missing failure context."}
			if err := r.prepareCIFeedback(context.Background(), dir, j); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "changed eligible context":
				j.Outcome.Limitations[0] = "Missing failure details for a different check."
			case "removed context":
				j.Outcome.Limitations = nil
			case "auth":
				j.Outcome.Limitations[0] = "Authentication requires attention."
			case "access":
				j.Outcome.Limitations[0] = "Access denied to CI evidence."
			case "requirements":
				j.Outcome.Limitations[0] = "Product requirements are missing."
			}
			if j.CIFeedbackRecoveries[0].BlockEvidence.Limitations[0] != "Missing failure context." {
				t.Fatal("audit evidence aliases mutable native limitations")
			}
			if err := ValidateCIFeedbackResume(*j, supplementalContext); err == nil {
				t.Fatal("changed limitation admitted")
			}
			beforeCI := pub.ci
			if err := r.Run(context.Background(), dir, j, ""); err == nil || len(p.calls) != 0 || pub.ci != beforeCI {
				t.Fatal("changed limitation reached repair stage", err)
			}
			if err := Save(dir, j); err == nil {
				t.Fatal("changed native evidence persisted against frozen pending audit")
			}
			loaded, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			if loaded.CIFeedbackRecoveries[0].BlockEvidence.Limitations[0] != "Missing failure context." || loaded.Attempt != 12 || loaded.CIRepairBudget.Used != 2 {
				t.Fatal("denied drift altered saved grant")
			}
			if kind == "auth" || kind == "access" || kind == "requirements" {
				// The same hold must refuse a fresh admission without an audit too.
				j.CIFeedbackRecoveries = nil
				j.State, j.ResumeState, j.StopReason = "blocked", "implementing", j.Outcome.Summary
				if err := ValidateCIFeedbackResume(*j, supplementalContext); err == nil {
					t.Fatal("other hold in limitations granted fresh recovery")
				}
			}
		})
	}
}

func TestCIFeedbackLimitationsDriftDuringStatusCannotDispatch(t *testing.T) {
	dir, j, r, p, _ := ciFeedbackRun(t)
	j.Outcome.Limitations = []string{"Missing failure context."}
	r.Provider = ciFeedbackStatusMutation{p, func() { j.Outcome.Limitations[0] = "Missing failure details for a different check." }}
	if err := r.Run(context.Background(), dir, j, ""); err == nil || len(p.calls) != 0 {
		t.Fatal("native evidence drift during status dispatched context", err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.CIFeedbackRecoveries[0].DispatchAttempt != 0 || loaded.CIFeedbackRecoveries[0].BlockEvidence.Limitations[0] != "Missing failure context." || loaded.CIRepairBudget.Used != 2 {
		t.Fatal("dispatch fence accepted changed blocked evidence")
	}
}

func TestCIFeedbackBeta55AuditCompatibility(t *testing.T) {
	for _, kind := range []string{"pending", "completed", "dispatched"} {
		t.Run(kind, func(t *testing.T) {
			dir, j, r, p, _ := ciFeedbackRun(t)
			j.Outcome.Limitations = []string{"Missing failure context."}
			if err := r.prepareCIFeedback(context.Background(), dir, j); err != nil {
				t.Fatal(err)
			}
			f := &j.CIFeedbackRecoveries[0]
			f.BlockEvidence = nil // beta55 persisted only BlockSummary.
			if kind != "pending" {
				j.Attempt++
				f.DispatchAttempt = j.Attempt
				f.Text = ""
				f.Completed = kind == "completed"
			}
			if err := Save(dir, j); err != nil {
				t.Fatal("beta55 audit cannot save", err)
			}
			loaded, err := Load(dir)
			if err != nil || loaded.CIFeedbackRecoveries[0].BlockEvidence != nil {
				t.Fatal("legacy audit changed during load", err)
			}
			r.CIFeedback = ""
			if kind == "pending" {
				p.outcomes = []Outcome{loaded.Outcome}
				if err := r.Run(context.Background(), dir, &loaded, ""); !errors.Is(err, ErrStopped) || len(p.calls) != 1 || !loaded.CIFeedbackRecoveries[0].Completed || loaded.CIFeedbackRecoveries[0].BlockEvidence == nil || loaded.CIRepairBudget.Used != 2 {
					t.Fatal("eligible beta55 pending audit could not resume once", err)
				}
			} else {
				if err := ValidateCIFeedbackResume(loaded, supplementalContext); err == nil || len(p.calls) != 0 {
					t.Fatal("legacy completed/interrupted context admitted again")
				}
				if err := Save(dir, &loaded); err != nil {
					t.Fatal(err)
				}
				retained, err := Load(dir)
				if err != nil || retained.CIFeedbackRecoveries[0].BlockEvidence != nil || retained.CIFeedbackRecoveries[0].SHA256 != f.SHA256 || retained.CIFeedbackRecoveries[0].DispatchAttempt != f.DispatchAttempt {
					t.Fatal("legacy history was rewritten", err)
				}
			}
		})
	}
}

func TestCIFeedbackRetainsCompletedEmptySourceArtifacts(t *testing.T) {
	for _, location := range []string{"verification", "formatter preparation"} {
		for _, kind := range []string{"completed", "pending key", "wrong key", "failed", "nonempty diff", "wrong head", "wrong tree", "digest", "size"} {
			t.Run(location+"/"+kind, func(t *testing.T) {
				dir, j, r, p, _ := ciFeedbackRun(t)
				request := sourcePatchRequest()
				if location == "formatter preparation" {
					request.ID = "formatter-context"
				}
				artifact := patchFixtureResult("")
				evidence := VerificationResult{Request: request, Head: testHead, Tree: testTree, Passed: true, SourcePatch: artifact}
				evidence.Key = verificationKey(j, request, testHead, testTree)
				switch kind {
				case "pending key":
					evidence.Key = ""
				case "wrong key":
					evidence.Key = strings.Repeat("f", 64)
				case "failed":
					evidence.Passed = false
				case "nonempty diff":
					evidence.SourcePatch = patchFixtureResult("disposable correction")
					evidence.Passed = false
				case "wrong head":
					artifact.Head = testBase
				case "wrong tree":
					artifact.Tree = testBase
				case "digest":
					artifact.DiffSHA256 = strings.Repeat("f", 64)
				case "size":
					artifact.Diff = strings.Repeat("x", maxSourcePatchDiff+1)
					artifact.DiffSHA256 = feedbackHash(artifact.Diff)
					evidence.Passed = false
				}
				if location == "verification" {
					j.Verification = []VerificationResult{evidence}
				} else {
					j.FormatterPreparation = []VerificationResult{evidence}
				}
				if kind != "completed" {
					if err := ValidateCIFeedbackResume(*j, supplementalContext); err == nil {
						t.Fatal("pending/invalid source correction admitted", kind)
					}
					return
				}
				if err := Save(dir, j); err != nil {
					t.Fatal(err)
				}
				loaded, err := Load(dir)
				if err != nil {
					t.Fatal(err)
				}
				if err := r.Run(context.Background(), dir, &loaded, ""); !errors.Is(err, ErrStopped) || len(p.calls) != 1 {
					t.Fatal("completed empty artifact blocked supplemental recovery", err)
				}
				retained := loaded.Verification
				if location == "formatter preparation" {
					retained = loaded.FormatterPreparation
				}
				if len(retained) != 1 || retained[0].Key != evidence.Key || !retained[0].Passed || retained[0].SourcePatch.Diff != "" || retained[0].Head != testHead || retained[0].Tree != testTree {
					t.Fatal("completed source evidence changed")
				}
			})
		}
	}
}

func ciFeedbackRun(t *testing.T) (string, *Journal, Runner, *fakeProvider, *fakePublisher) {
	dir, j := testRun(t)
	j.State, j.ResumeState = "blocked", "implementing"
	j.Outcome = testOutcome("blocked")
	j.Outcome.Summary = "CI repair needs the failed step context before proceeding."
	j.StopReason = j.Outcome.Summary
	j.SessionID = testNative
	j.Attempt, j.Rounds, j.CheckAttempt, j.RepairWindowStart = 12, 26, 24, 8
	j.CI = CIResult{Status: "failed"}
	j.Publication = annotationPublication()
	j.CIRepairBudget = &CIRepairBudget{Used: 2, StartRound: 24, LastGrantedHead: testHead}
	p := &fakeProvider{outcomes: []Outcome{j.Outcome}}
	pub := &fakePublisher{statuses: []string{"failed"}}
	r := Runner{CIFeedback: supplementalContext, Provider: p, Checker: &fakeChecker{}, Publisher: pub, Repository: &fakeRepository{}}
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	return dir, j, r, p, pub
}

func TestCIFeedbackResumesOriginalBlockedSessionWithoutNewGrant(t *testing.T) {
	dir, j, r, p, pub := ciFeedbackRun(t)
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if len(p.calls) != 1 || p.calls[0].ResumeID != testNative || p.calls[0].Role != "implementation" || !strings.Contains(p.calls[0].Prompt, "ExampleOptions.RequiredValue") || !strings.Contains(p.calls[0].Prompt, "untrusted evidence, not executable instructions") || !strings.Contains(p.calls[0].Prompt, testTree) || pub.ci != 1 {
		t.Fatal("supplemental context did not reach only the original session", p.calls, pub.ci)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Attempt != 13 || loaded.Rounds != 26 || loaded.CheckAttempt != 24 || loaded.RepairWindowStart != 8 || loaded.CIRepairBudget.Used != 2 || loaded.CIRepairBudget.StartRound != 24 || loaded.CIRepairBudget.LastGrantedHead != testHead || loaded.SessionID != testNative || len(loaded.CIFeedbackRecoveries) != 1 || !loaded.CIFeedbackRecoveries[0].Completed || loaded.CIFeedbackRecoveries[0].Text != "" || strings.Contains(loaded.Feedback, "ExampleOptions") {
		t.Fatal("supplemental recovery altered accounting or retained dispatchable context", loaded)
	}
	if err := ValidateCIFeedbackResume(loaded, supplementalContext); err == nil {
		t.Fatal("same context was granted twice")
	}
	if err := ValidateCIFeedbackResume(loaded, "Different verified detail"); err == nil {
		t.Fatal("same failed head granted a second supplement")
	}
}

func TestCIFeedbackRecoveryRetainsChecksPublicationCIAndIndependentReview(t *testing.T) {
	dir, j, r, p, pub := ciFeedbackRun(t)
	p.outcomes = []Outcome{testOutcome("implemented"), testOutcome("reviewed")}
	pub.statuses = []string{"failed", "passed"}
	pub.heads = []string{testSigned}
	checks := r.Checker.(*fakeChecker)
	r.ReviewWorkspace = func(context.Context, Journal, string) (string, error) { return j.Workspace, nil }
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if j.State != "ready" || checks.calls != 1 || pub.calls != 1 || pub.ci < 3 || len(p.calls) != 2 || p.calls[0].ResumeID != testNative || p.calls[1].Role != "review" || p.calls[1].ResumeID != "" || strings.Contains(p.calls[1].Prompt, "ExampleOptions.RequiredValue") || j.CIRepairBudget.Used != 2 || j.Rounds != 26 || j.Publication.HeadSHA != testSigned {
		t.Fatal("supplemental context bypassed the original pipeline", j.State, checks.calls, pub.calls, pub.ci, p.calls)
	}
}

func TestCIFeedbackRejectsForbiddenStoppedRunsBeforeDispatch(t *testing.T) {
	for _, kind := range []string{"question", "source", "inputs", "review", "grant", "failed", "operational", "auth", "policy", "usage", "turn limit", "session", "pending verification", "specification", "tool", "source summary", "indirect refusal", "mixed specification"} {
		t.Run(kind, func(t *testing.T) {
			dir, j, r, p, pub := ciFeedbackRun(t)
			switch kind {
			case "question":
				j.Outcome.Questions = []string{"Which behavior?"}
			case "source":
				j.Outcome.RepositoryRequests = []CompanionRepositoryRequest{{ID: "source"}}
			case "inputs":
				j.PendingInputs = &PendingInputRecovery{}
			case "review":
				j.ResumeState = "reviewing"
			case "grant":
				j.CIRepairBudget.LastGrantedHead = testBase
			case "failed":
				j.Outcome.Status = "failed"
			case "operational":
				j.StopReason = "provider unavailable"
			case "auth":
				j.Outcome.Summary = "Authentication requires attention."
				j.StopReason = j.Outcome.Summary
			case "policy":
				j.Outcome.Summary = "Policy refusal."
				j.StopReason = j.Outcome.Summary
			case "usage":
				j.Outcome.Summary = "Usage limit reached."
				j.StopReason = j.Outcome.Summary
			case "turn limit":
				j.Attempt = defaultNativeTurnLimit
			case "session":
				j.SessionID = ""
			case "pending verification":
				j.Verification = []VerificationResult{{}}
			case "specification":
				j.Outcome.Summary = "The required specification lacks behavior details."
				j.StopReason = j.Outcome.Summary
			case "tool":
				j.Outcome.Summary = "The required compiler is unavailable."
				j.StopReason = j.Outcome.Summary
			case "source summary":
				j.Outcome.Summary = "The companion repository source is missing."
				j.StopReason = j.Outcome.Summary
			case "indirect refusal":
				j.Outcome.Summary = "CI context is missing but I cannot help with this harmful request."
				j.StopReason = j.Outcome.Summary
			case "mixed specification":
				j.Outcome.Summary = "CI still fails, but I need product requirements and implementation details before changing behavior."
				j.StopReason = j.Outcome.Summary
			}
			// Do not persist deliberately invalid test journals: the controller
			// checks eligibility before it can save or execute anything.
			if err := r.Run(context.Background(), dir, j, ""); err == nil || len(p.calls) != 0 || pub.ci != 0 || len(j.CIFeedbackRecoveries) != 0 || j.CIRepairBudget.Used != 2 {
				t.Fatal("forbidden hold received context or grant", err)
			}
		})
	}
}

func TestCIFeedbackRevalidatesCIAndCandidateWithoutGrant(t *testing.T) {
	for _, kind := range []string{"passed", "pending", "missing", "transport", "dirty"} {
		t.Run(kind, func(t *testing.T) {
			dir, j, r, p, pub := ciFeedbackRun(t)
			if kind == "transport" {
				pub.ciErr = errors.New("offline unavailable")
			} else if kind == "dirty" {
				r.Repository = &fakeRepository{revisions: []Revision{{Head: testHead, Tree: testTree, Clean: false}}}
			} else {
				pub.statuses = []string{kind}
			}
			if err := r.Run(context.Background(), dir, j, ""); err == nil || len(p.calls) != 0 || len(j.CIFeedbackRecoveries) != 0 || j.CIRepairBudget.Used != 2 || j.Attempt != 12 {
				t.Fatal("stale/unknown boundary admitted", err)
			}
		})
	}
}

func TestCIFeedbackRejectsCandidateChangeDuringProviderStatus(t *testing.T) {
	dir, j, r, p, _ := ciFeedbackRun(t)
	repo := r.Repository.(*fakeRepository)
	r.Provider = ciFeedbackStatusMutation{p, func() { repo.revisions = []Revision{{Head: testBase, Tree: testTree, Clean: true}} }}
	if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) || len(p.calls) != 0 {
		t.Fatal("candidate drift during status dispatched context", err)
	}
	if len(j.CIFeedbackRecoveries) != 1 || j.CIFeedbackRecoveries[0].DispatchAttempt != 0 || j.CIFeedbackRecoveries[0].Text != supplementalContext || j.CIRepairBudget.Used != 2 || j.Rounds != 26 {
		t.Fatal("candidate drift dispatched or renewed context grant")
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	r.Provider = p
	r.CIFeedback = ""
	repo.revisions = nil
	if err := r.Run(context.Background(), dir, &loaded, ""); !errors.Is(err, ErrStopped) || len(p.calls) != 1 || loaded.CIFeedbackRecoveries[0].DispatchAttempt != 14 || !loaded.CIFeedbackRecoveries[0].Completed || loaded.CIRepairBudget.Used != 2 || loaded.Rounds != 26 {
		t.Fatal("restored predispatch candidate could not recover exactly once", err)
	}
}

func TestCIFeedbackRejectsRetainedInputChangeDuringProviderStatus(t *testing.T) {
	for _, kind := range []string{"source", "check"} {
		t.Run(kind, func(t *testing.T) {
			dir, j, r, p, _ := ciFeedbackRun(t)
			root := j.Workspace
			if kind == "check" {
				root = filepath.Join(dir, "check-inputs")
				if err := os.Mkdir(root, 0700); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(root, "selected.txt")
			if err := os.WriteFile(path, []byte("frozen input"), 0600); err != nil {
				t.Fatal(err)
			}
			input := Input{Path: "selected.txt", SHA256: feedbackHash("frozen input")}
			if kind == "source" {
				j.Plan.Inputs = []Input{input}
			} else {
				j.Plan.CheckInputs = []Input{input}
			}
			if err := Save(dir, j); err != nil {
				t.Fatal(err)
			}
			r.Provider = ciFeedbackStatusMutation{p, func() {
				if err := os.WriteFile(path, []byte("changed input"), 0600); err != nil {
					t.Fatal(err)
				}
			}}
			if err := r.Run(context.Background(), dir, j, ""); !errors.Is(err, ErrStopped) || len(p.calls) != 0 || j.CIFeedbackRecoveries[0].DispatchAttempt != 0 || j.CIRepairBudget.Used != 2 {
				t.Fatal("retained input drift reached original session", err)
			}
		})
	}
}

func TestCIFeedbackPendingRecoveryAndDispatchFence(t *testing.T) {
	dir, j, r, p, pub := ciFeedbackRun(t)
	if err := r.prepareCIFeedback(context.Background(), dir, j); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ValidateCIFeedbackResume(loaded, "changed") == nil {
		t.Fatal("pending context replaced")
	}
	stale := r
	stale.Repository = &fakeRepository{revisions: []Revision{{Head: testBase, Tree: testTree, Clean: true}}}
	if err := stale.Run(context.Background(), dir, &loaded, ""); err == nil || len(p.calls) != 0 {
		t.Fatal("pending candidate drift dispatched", err)
	}
	r.CIFeedback = ""
	p.executeErr = errors.New("interrupted native call")
	if err := r.Run(context.Background(), dir, &loaded, ""); !errors.Is(err, ErrStopped) {
		t.Fatal(err)
	}
	if len(p.calls) != 1 || len(loaded.CIFeedbackRecoveries) != 1 || loaded.CIFeedbackRecoveries[0].DispatchAttempt != 13 || loaded.CIFeedbackRecoveries[0].Text != "" || loaded.CIFeedbackRecoveries[0].Completed {
		t.Fatal("dispatch fence missing")
	}
	loaded, err = Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	beforeCI := pub.ci
	if err := r.Run(context.Background(), dir, &loaded, ""); err == nil || len(p.calls) != 1 || pub.ci != beforeCI || loaded.Attempt != 13 || loaded.CIRepairBudget.Used != 2 {
		t.Fatal("interrupted context was dispatched twice", err)
	}
}

func TestCIFeedbackAdmitsFullerControllerEvidenceForPersistedCIRepair(t *testing.T) {
	_, j, _, _, _ := ciFeedbackRun(t)
	j.Outcome.Summary = "Inspected the reported locations; source remains unchanged. CI repair requires fuller controller evidence before implementation can continue."
	j.Outcome.Limitations = []string{"Generic analyzer findings lack rule IDs and descriptions.", "The Trivy excerpt omits actual vulnerability findings.", "Coverage report is missing; uncovered lines are unknown.", "The public analyzer endpoint was inaccessible through the available tool."}
	j.StopReason = j.Outcome.Summary
	if err := validateCIFeedbackEligibility(*j, ""); err != nil {
		t.Fatal("corroborated fuller-evidence block refused", err)
	}
	j.Outcome.Limitations = append(j.Outcome.Limitations, "Provider access denied.")
	if err := validateCIFeedbackEligibility(*j, ""); err == nil {
		t.Fatal("provider hold admitted")
	}
}
