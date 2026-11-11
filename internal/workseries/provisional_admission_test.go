package workseries

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/workrun"
)

func admissionFixture(t *testing.T) (string, *State, string, workrun.Journal) {
	t.Helper()
	dir, state, runDir, j, _ := runtimeTransactionFixture(t)
	j.Attempt = 1
	j.Outcome = workrun.Outcome{Status: "reviewed"}
	coverage := &workrun.ReviewCoverage{BaseSHA: j.Publication.BaseSHA, HeadSHA: j.Publication.HeadSHA, Scope: "full", Gaps: []string{}}
	j.ReviewReports = []workrun.ReviewReport{{Publication: j.Publication, Attempt: j.Attempt, Coverage: coverage}}
	j.CIHeldReview = &workrun.CIHeldReview{Publication: j.Publication, HoldReason: "CI unavailable", StartedAt: time.Now().UTC()}
	data, _ := json.Marshal(struct {
		Request           workrun.VerificationRequest
		Head, Tree, Image string
		Inputs            []workrun.Input
		Commands          [][]string
		Docker            bool
		Daemon            string
		DaemonMode        string `json:",omitempty"`
	}{workrun.VerificationRequest{}, j.Evidence.Head, j.Evidence.Tree, j.ImageID, j.Plan.CheckInputs, j.Plan.Checks, j.Plan.DockerTests, j.Plan.DaemonImage, j.Plan.DaemonMode})
	sum := sha256.Sum256(data)
	j.Evidence.Key = hex.EncodeToString(sum[:])
	j.PRFeedback = &workrun.PRFeedbackHistory{Version: 1, Cycle: 1, CandidateStart: j.Evidence.Head, Entries: []workrun.PRFeedbackEntry{}, CIHeldScan: &workrun.CIHeldFeedbackScan{RunID: j.ID, Publication: j.Publication, CandidateHead: j.Evidence.Head, CandidateTree: j.Evidence.Tree, CheckKey: j.Evidence.Key, Attempt: j.Attempt, ScannedAt: time.Now().UTC()}}
	if err := workrun.Save(runDir, &j); err != nil {
		t.Fatal(err)
	}
	return dir, state, runDir, j
}

func TestProvisionalGrantPreviewRevokeAndLiveVerification(t *testing.T) {
	dir, state, runDir, j := admissionFixture(t)
	featurePath, runPath := filepath.Join(dir, "journal.json"), filepath.Join(runDir, "journal.json")
	beforeFeature, _ := os.ReadFile(featurePath)
	beforeRun, _ := os.ReadFile(runPath)
	checks := 0
	verify := func(_ context.Context, _ State, _ workrun.Journal, proof *workrun.ProvisionalParentProof) error {
		checks++
		if proof.Publication != j.Publication {
			t.Fatal("wrong publication")
		}
		return nil
	}
	preview, err := SetProvisionalParent(context.Background(), state.Plan.Root, state.Plan.Reference, "01-one.md", false, true, verify)
	if err != nil {
		t.Fatal(err)
	}
	afterFeature, _ := os.ReadFile(featurePath)
	afterRun, _ := os.ReadFile(runPath)
	if string(beforeFeature) != string(afterFeature) || string(beforeRun) != string(afterRun) || preview.Proof.RunID != j.ID {
		t.Fatal("preview mutated state")
	}
	if _, err := SetProvisionalParent(context.Background(), state.Plan.Root, state.Plan.Reference, "01-one.md", false, false, verify); err != nil {
		t.Fatal(err)
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	result := saved.Results["01-one.md"]
	if result.State != "blocked" || result.HoldStage != "ci" || result.Provisional == nil || result.Provisional.Proof.Publication != j.Publication || checks != 2 {
		t.Fatal("grant lost hold or identity")
	}
	if _, err := SetProvisionalParent(context.Background(), state.Plan.Root, state.Plan.Reference, "01-one.md", true, false, nil); err != nil {
		t.Fatal(err)
	}
	saved, err = Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Results["01-one.md"].Provisional != nil || saved.Results["01-one.md"].State != "blocked" || len(saved.ProvisionalAudit) != 2 || saved.ProvisionalAudit[1].Action != "revoke" {
		t.Fatal("revoke lost state or audit")
	}
	afterRun, _ = os.ReadFile(runPath)
	if string(beforeRun) != string(afterRun) {
		t.Fatal("grant or revoke changed run checkpoint")
	}
}

func TestProvisionalGrantRefreshesChangedHeadOnlyBeforeDependentStarts(t *testing.T) {
	for _, started := range []bool{false, true} {
		t.Run(fmt.Sprint(started), func(t *testing.T) {
			dir, state, _, j := admissionFixture(t)
			parent := state.Results["01-one.md"]
			parent.HeadSHA = sha("old-published-head")
			state.Results["01-one.md"] = parent
			if started {
				state.Results["02-two.md"] = Result{RunID: strings.Repeat("2", 24), State: "prepared"}
			}
			if err := Save(dir, state); err != nil {
				t.Fatal(err)
			}
			calls := 0
			verify := func(context.Context, State, workrun.Journal, *workrun.ProvisionalParentProof) error {
				calls++
				return nil
			}
			_, err := SetProvisionalParent(context.Background(), state.Plan.Root, state.Plan.Reference, "01-one.md", false, false, verify)
			if started {
				if err == nil || calls != 0 {
					t.Fatal("stale head replaced despite existing dependent")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			saved, err := Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Results["01-one.md"].HeadSHA != j.Publication.HeadSHA || saved.Results["01-one.md"].Provisional == nil || calls != 1 {
				t.Fatal("validated new head not recorded")
			}
		})
	}
}
