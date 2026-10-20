package workrun

import (
	"context"
	"reflect"
	"strings"
	"testing"
)

func TestVerifiedSelfReviewBaselineFocusesSameSessionFeedback(t *testing.T) {
	_, j, r, repo := correctionReviewFixture(t)
	j.SelfReview = &SelfReviewBoundary{Head: testHead, Tree: testTree, ContextKey: incrementalContextKey(j), SessionID: j.SessionID}
	text := r.implementationSelfReviewPrompt(context.Background(), j, j.Workspace, j.SessionID)
	if !strings.Contains(text, `"mode":"correction"`) || !strings.Contains(text, "src/a.cs") {
		t.Fatal("repair scope missing", text)
	}
	repo.current.Tree = testTree
	text = r.implementationSelfReviewPrompt(context.Background(), j, j.Workspace, j.SessionID)
	if !strings.Contains(text, `"mode":"unchanged"`) {
		t.Fatal("identical content requested full self-review")
	}
	if text := r.implementationSelfReviewPrompt(context.Background(), j, j.Workspace, "different-session"); text != "" {
		t.Fatal("cross-session self-review reuse")
	}
	j.Instructions += "new requirement"
	if text := r.implementationSelfReviewPrompt(context.Background(), j, j.Workspace, j.SessionID); text != "" {
		t.Fatal("changed scope reused self-review")
	}
}

func TestSuccessfulFeedbackIsCompactButFailuresAndDurableEvidenceRemain(t *testing.T) {
	success := CommandOutput{Completed: true, ExitCode: 0, Command: 1, CombinedOutput: strings.Repeat("successful detail ", 2000), TotalBytes: 36000}
	failure := CommandOutput{Completed: true, ExitCode: 1, Command: 2, CombinedOutput: "src/Example.cs:17 failed: result was discarded", TotalBytes: 47}
	warning := CommandOutput{Completed: true, ExitCode: 0, Command: 3, CombinedOutput: "actionable retained diagnostic", FailureBytes: 30, TotalBytes: 30}
	_, j := testRun(t)
	j.Evidence.Output = []CommandOutput{success, failure, warning}
	original := append([]CommandOutput(nil), j.Evidence.Output...)
	feedback := verificationFeedback(j)
	if strings.Contains(feedback, "successful detail") || !strings.Contains(feedback, "result was discarded") || !strings.Contains(feedback, "actionable retained diagnostic") {
		t.Fatal("feedback lost failure detail or repeated success stdout")
	}
	if !reflect.DeepEqual(j.Evidence.Output, original) {
		t.Fatal("prompt projection mutated durable evidence")
	}
}
