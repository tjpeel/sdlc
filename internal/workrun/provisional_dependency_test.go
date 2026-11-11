package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/filelock"
)

type provisionalRemoteFake struct {
	observation RemotePR
	branch      BranchSnapshot
	comments    []PRFeedbackComment
}

func (p *provisionalRemoteFake) Observe(context.Context, Plan, Publication) (RemotePR, error) {
	return p.observation, nil
}
func (p *provisionalRemoteFake) Branch(context.Context, Plan, string) (BranchSnapshot, error) {
	return p.branch, nil
}
func (p *provisionalRemoteFake) Feedback(context.Context, Plan, Publication) ([]PRFeedbackComment, error) {
	return p.comments, nil
}

func TestProvisionalDependencyRechecksGrantRunPRAndComments(t *testing.T) {
	oldDir, j, _, runner := ciHeldFeedbackFixture(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id, ticket := strings.Repeat("a", 24), "01-parent.md"
	runDir := filepath.Join(root, ".sdlc", "work", "TASK-1", "runs", "01-parent", id)
	if err := os.MkdirAll(filepath.Dir(runDir), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldDir, runDir); err != nil {
		t.Fatal(err)
	}
	j.ID, j.Plan.Root, j.Plan.Ticket, j.Workspace = id, root, ticket, filepath.Join(runDir, "workspace")
	j.Evidence.Key = verificationKey(j, VerificationRequest{}, j.Evidence.Head, j.Evidence.Tree)
	if err := Save(runDir, j); err != nil {
		t.Fatal(err)
	}
	if err := runner.beginPRFeedback(context.Background(), runDir, j); !errors.Is(err, ErrStopped) {
		t.Fatalf("scan: %v", err)
	}
	proof, err := ProvisionalProofForCIHold(*j)
	if err != nil {
		t.Fatal(err)
	}
	series := filepath.Join(root, ".sdlc", "work", "TASK-1", "series")
	if err := os.MkdirAll(series, 0700); err != nil {
		t.Fatal(err)
	}
	writeState := func(mark bool) {
		t.Helper()
		result := map[string]any{"State": "blocked"}
		if mark {
			result["provisional"] = map[string]any{"proof": proof}
		}
		data, _ := json.Marshal(map[string]any{"Results": map[string]any{ticket: result}})
		if err := os.WriteFile(filepath.Join(series, "journal.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	writeState(true)
	plan := Plan{Root: root, Reference: "TASK-1", Repository: "example/project", Base: proof.Branch, BaseSHA: proof.Publication.HeadSHA, ProvisionalParent: &proof}
	remote := &provisionalRemoteFake{observation: RemotePR{State: "OPEN", Base: proof.Base, BaseSHA: proof.Publication.BaseSHA, HeadSHA: proof.Publication.HeadSHA}, branch: BranchSnapshot{Branch: proof.Branch, SHA: proof.Publication.HeadSHA}}
	if err := ValidateProvisionalDependency(context.Background(), plan, remote); err != nil {
		t.Fatal(err)
	}
	j.CI.Status = "failed"
	if err := Save(runDir, j); err != nil {
		t.Fatal(err)
	}
	if err := ValidateProvisionalDependency(context.Background(), plan, remote); err != nil {
		t.Fatalf("same-head CI status change invalidated parent: %v", err)
	}
	lock, err := filelock.AcquireExisting(filepath.Join(runDir, "run.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateProvisionalDependency(context.Background(), plan, remote); !errors.Is(err, ErrDependencyInvalid) {
		t.Fatal("active parent controller accepted")
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	remote.branch.SHA = testTree
	if err := ValidateProvisionalDependency(context.Background(), plan, remote); !errors.Is(err, ErrDependencyInvalid) {
		t.Fatal("moved branch accepted")
	}
	remote.branch.SHA = proof.Publication.HeadSHA
	remote.comments = []PRFeedbackComment{generalFeedback("comment:new", "new human feedback")}
	if err := ValidateProvisionalDependency(context.Background(), plan, remote); !errors.Is(err, ErrDependencyInvalid) {
		t.Fatal("new comment accepted")
	}
	remote.comments = nil
	writeState(false)
	if err := ValidateProvisionalDependency(context.Background(), plan, remote); !errors.Is(err, ErrDependencyInvalid) {
		t.Fatal("revoked grant accepted")
	}
}
