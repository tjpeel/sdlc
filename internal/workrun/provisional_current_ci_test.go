package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type reviewedParentRemote struct {
	*provisionalRemoteFake
	ci string
}

func (r *reviewedParentRemote) Checks(context.Context, Plan, Publication) (CIResult, error) {
	return CIResult{Status: r.ci}, nil
}

func TestReviewedProvisionalDependencyRequiresCurrentPassingCI(t *testing.T) {
	oldDir, j, _, _ := ciHeldFeedbackFixture(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, ticket := strings.Repeat("b", 24), "01-parent.md"
	runDir := filepath.Join(root, ".sdlc", "work", "TASK-1", "runs", "01-parent", id)
	if err := os.MkdirAll(filepath.Dir(runDir), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldDir, runDir); err != nil {
		t.Fatal(err)
	}
	j.ID, j.Plan.Root, j.Plan.Ticket, j.Workspace = id, root, ticket, filepath.Join(runDir, "workspace")
	j.Evidence.Key = verificationKey(j, VerificationRequest{}, j.Evidence.Head, j.Evidence.Tree)
	j.State, j.ResumeState, j.StopReason = "ready", "", ""
	j.CI.Status = "passed"
	j.CIHeldReview = nil
	j.PRFeedback = &PRFeedbackHistory{Version: 1, Cycle: 1, CandidateStart: j.Evidence.Head, CompletedHead: j.Publication.HeadSHA}
	if err := Save(runDir, j); err != nil {
		t.Fatal(err)
	}
	proof, err := ProvisionalProofForParent(*j)
	if err != nil {
		t.Fatal(err)
	}
	series := filepath.Join(root, ".sdlc", "work", "TASK-1", "series")
	if err := os.MkdirAll(series, 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"Results": map[string]any{ticket: map[string]any{"State": "blocked", "provisional": map[string]any{"proof": proof}}}})
	if err := os.WriteFile(filepath.Join(series, "journal.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	plan := Plan{Root: root, Reference: "TASK-1", Repository: "example/project", Base: proof.Branch, BaseSHA: proof.Publication.HeadSHA, ProvisionalParent: &proof}
	remote := &reviewedParentRemote{provisionalRemoteFake: &provisionalRemoteFake{observation: RemotePR{State: "OPEN", Base: proof.Base, BaseSHA: proof.Publication.BaseSHA, HeadSHA: proof.Publication.HeadSHA}, branch: BranchSnapshot{Branch: proof.Branch, SHA: proof.Publication.HeadSHA}}, ci: "passed"}
	if err := ValidateProvisionalDependency(context.Background(), plan, remote); err != nil {
		t.Fatal(err)
	}
	remote.ci = "failed"
	if err := ValidateProvisionalDependency(context.Background(), plan, remote); !errors.Is(err, ErrDependencyInvalid) {
		t.Fatal("failed current CI accepted")
	}
}
