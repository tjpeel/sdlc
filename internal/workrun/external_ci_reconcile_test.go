package workrun

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

type externalCIErrorPublisher struct{ *externalRebasePublisher }

func (*externalCIErrorPublisher) Checks(context.Context, Plan, Publication) (CIResult, error) {
	return CIResult{}, errors.New("CI query failed")
}

func TestExternalCIErrorReconcilesOnlyVerifiedBaseMovement(t *testing.T) {
	for _, scenario := range []string{"moved", "unchanged", "head changed", "pending input"} {
		t.Run(scenario, func(t *testing.T) {
			dir, j, p, baseRepo := externalMergeFixture(t)
			upstream := &resumeTestPublisher{remote: RemotePR{State: "OPEN", Base: j.Plan.Base, BaseSHA: j.Plan.BaseSHA, HeadSHA: j.Publication.HeadSHA}, branchSHA: j.Plan.BaseSHA}
			pub := &externalCIErrorPublisher{&externalRebasePublisher{externalTestPublisher: p, upstream: upstream}}
			repo := &externalRebaseRepository{baseRepo}
			runner := Runner{Publisher: pub, Repository: repo, ExternalPRURL: p.observation.Boundary.IncomingURL, CheckUpstream: true, Output: io.Discard}
			if err := runner.mergeExternalPR(context.Background(), dir, j); err != nil {
				t.Fatal(err)
			}
			j.State = "ci"
			upstream.remote.HeadSHA = j.Publication.HeadSHA
			j.Evidence = CheckEvidence{Head: repo.revision.Head, Tree: repo.revision.Tree, Passed: true}
			if scenario != "unchanged" {
				upstream.branchSHA = testTree
			}
			if scenario == "head changed" {
				upstream.remote.HeadSHA = strings.Repeat("a", 40)
			}
			if scenario == "pending input" {
				j.DeferredAnswer = "pending"
			}
			err := runner.checkCI(context.Background(), dir, j)
			if scenario == "moved" {
				if err != nil || j.Plan.BaseSHA != testTree || j.State != "implementing" || j.Evidence.Passed {
					t.Fatalf("reconciliation failed: state=%s err=%v", j.State, err)
				}
			} else if !errors.Is(err, ErrStopped) || j.State != "blocked" || j.Plan.BaseSHA == testTree {
				t.Fatalf("unsafe or unrelated failure bypassed: state=%s err=%v", j.State, err)
			}
		})
	}
}
