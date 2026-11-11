package workseries

import (
	"context"
	"fmt"
	"path/filepath"
	"time"

	"github.com/tjpeel/sdlc/internal/workrun"
)

// ProvisionalVerifier must check frozen inputs, the live PR and branch, the
// candidate, and a fresh complete feedback scan without starting work.
type ProvisionalVerifier func(context.Context, State, workrun.Journal, *workrun.ProvisionalParentProof) error

type ProvisionalPreview struct {
	Reference string                         `json:"reference"`
	Ticket    string                         `json:"ticket"`
	Proof     workrun.ProvisionalParentProof `json:"proof"`
	Revoke    bool                           `json:"revoke"`
	DryRun    bool                           `json:"dry_run"`
}

// SetProvisionalParent updates only the feature journal while owning its lock
// and the parent run lock. Child controllers must independently check the
// saved proof and live parent at each execution boundary.
func SetProvisionalParent(ctx context.Context, root, reference, ticket string, revoke, dryRun bool, verify ProvisionalVerifier) (ProvisionalPreview, error) {
	preview := ProvisionalPreview{Reference: reference, Ticket: ticket, Revoke: revoke, DryRun: dryRun}
	if filepath.Base(ticket) != ticket {
		return preview, fmt.Errorf("ticket must be a numbered file name")
	}
	dir, _, err := amendmentPaths(root, reference)
	if err != nil {
		return preview, err
	}
	lock, err := ownAmendmentFeature(dir)
	if err != nil {
		return preview, err
	}
	defer lock.Close()
	state, err := Load(dir)
	if err != nil {
		return preview, err
	}
	if state.Plan.Root != root || state.Plan.Reference != reference {
		return preview, fmt.Errorf("feature identity differs from the requested reference")
	}
	result, ok := state.Results[ticket]
	if !ok || result.RunID == "" {
		return preview, fmt.Errorf("ticket has no saved run")
	}
	if revoke {
		if result.Provisional == nil {
			return preview, fmt.Errorf("ticket has no provisional grant")
		}
		preview.Proof = result.Provisional.Proof
		if !dryRun {
			state.ProvisionalAudit = append(state.ProvisionalAudit, ProvisionalAudit{Proof: preview.Proof, At: time.Now().UTC(), Action: "revoke"})
			result.Provisional = nil
			state.Results[ticket] = result
			if err := Save(dir, &state); err != nil {
				return preview, err
			}
		}
		return preview, nil
	}
	if result.Provisional != nil {
		return preview, fmt.Errorf("ticket already has a provisional grant")
	}
	runDir, err := runtimeRunDir(root, reference, RuntimeMigrationEntry{Ticket: ticket, RunID: result.RunID})
	if err != nil {
		return preview, err
	}
	runLock, err := ownRuntimeRun(runDir)
	if err != nil {
		return preview, err
	}
	defer runLock.Close()
	j, err := workrun.Load(runDir)
	if err != nil {
		return preview, err
	}
	if j.ID != result.RunID || j.Plan.Root != root || j.Plan.Reference != reference || filepath.Base(j.Plan.Ticket) != ticket {
		return preview, fmt.Errorf("parent run identity differs from the feature")
	}
	proof, err := workrun.ProvisionalProofForCIHold(j)
	if err != nil {
		return preview, err
	}
	if result.State == "merged" || result.Branch != "" && result.Branch != proof.Branch || result.Base != "" && result.Base != j.Plan.Base || result.BaseSHA != "" && result.BaseSHA != proof.Publication.BaseSHA || result.PRNumber != 0 && result.PRNumber != proof.Publication.Number || result.URL != "" && result.URL != proof.Publication.URL {
		return preview, fmt.Errorf("parent feature result conflicts with its run")
	}
	if result.HeadSHA != "" && result.HeadSHA != proof.Publication.HeadSHA && startedProvisionalDescendant(state, ticket) {
		return preview, fmt.Errorf("parent head changed while dependent work exists; resume the feature to reconcile it before provisional admission")
	}
	if verify == nil {
		return preview, fmt.Errorf("provisional admission requires live parent and feedback verification")
	}
	before := proof
	if err := verify(ctx, state, j, &proof); err != nil {
		return preview, err
	}
	if !workrun.SameProvisionalBoundary(before, proof) || proof.CIHead != proof.Publication.HeadSHA || proof.CIStatus == "" || len(proof.CIIdentities) > 32768 {
		return preview, fmt.Errorf("live verifier changed the provisional publication boundary")
	}
	preview.Proof = proof
	if dryRun {
		return preview, nil
	}
	previousHead := result.HeadSHA
	result.Directory, result.Branch, result.Base = runDir, proof.Branch, j.Plan.Base
	result.BaseSHA, result.HeadSHA, result.PRNumber, result.URL = proof.Publication.BaseSHA, proof.Publication.HeadSHA, proof.Publication.Number, proof.Publication.URL
	result.State, result.HoldStage, result.StopReason = "blocked", "ci", j.StopReason
	now := time.Now().UTC()
	result.Provisional = &ProvisionalMark{Proof: proof, GrantedAt: now}
	state.Results[ticket] = result
	state.ProvisionalAudit = append(state.ProvisionalAudit, ProvisionalAudit{Proof: proof, PreviousHead: previousHead, At: now, Action: "grant"})
	if err := Save(dir, &state); err != nil {
		return preview, err
	}
	return preview, nil
}

func startedProvisionalDescendant(state State, parent string) bool {
	tickets := ticketMap(state.Plan.Tickets)
	var depends func(string, map[string]bool) bool
	depends = func(name string, seen map[string]bool) bool {
		if seen[name] {
			return false
		}
		seen[name] = true
		for _, dependency := range tickets[name].DependsOn {
			if dependency == parent || depends(dependency, seen) {
				return true
			}
		}
		return false
	}
	for name, result := range state.Results {
		if name != parent && result.RunID != "" && depends(name, map[string]bool{}) {
			return true
		}
	}
	return false
}
