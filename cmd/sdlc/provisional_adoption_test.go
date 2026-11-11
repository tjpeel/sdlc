package main

import (
	"strings"
	"testing"
	"time"

	"github.com/tjpeel/sdlc/internal/workrun"
	"github.com/tjpeel/sdlc/internal/workseries"
)

func TestFeatureAdoptionRetainsStaleProvisionalGrantForObserver(t *testing.T) {
	driver, result := featureAdoptionFixture(t, "blocked")
	proof := workrun.ProvisionalParentProof{RunID: result.RunID, Ticket: "01-selected.md", Branch: "old-branch", Base: "main", Publication: workrun.Publication{Number: 1, URL: "https://github.com/example/project/pull/1", BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40)}, CandidateHead: strings.Repeat("c", 40), CandidateTree: strings.Repeat("d", 40), CheckKey: "old-check", Attempt: 1, ScannedAt: time.Now().UTC(), CIStatus: "failed", CIHead: strings.Repeat("b", 40)}
	result.State, result.HoldStage, result.Branch, result.Base = "blocked", "ci", proof.Branch, proof.Base
	result.BaseSHA, result.HeadSHA, result.PRNumber, result.URL = proof.Publication.BaseSHA, proof.Publication.HeadSHA, proof.Publication.Number, proof.Publication.URL
	result.Provisional = &workseries.ProvisionalMark{Proof: proof, GrantedAt: time.Now().UTC()}
	state := workseries.State{Version: 1, Plan: driver.plan, Results: map[string]workseries.Result{"01-selected.md": result}}
	if err := driver.adoptRuns(&state); err != nil {
		t.Fatal(err)
	}
	got := state.Results["01-selected.md"]
	if got.Provisional == nil || got.Provisional.Proof != proof || got.HeadSHA != proof.Publication.HeadSHA {
		t.Fatal("stale parent grant vanished before observer invalidation")
	}
}
