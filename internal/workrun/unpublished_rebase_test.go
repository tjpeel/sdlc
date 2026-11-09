package workrun

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

type unpublishedTestPublisher struct {
	*resumeTestPublisher
	unsafe bool
}

func (p *unpublishedTestPublisher) VerifyUnpublished(context.Context, Plan) error {
	if p.unsafe {
		return fmt.Errorf("remote destination exists")
	}
	return nil
}

func unpublishedFixture(t *testing.T) (string, *Journal, *unpublishedTestPublisher, *resumeTestRepository) {
	t.Helper()
	dir, j := testRun(t)
	j.State, j.ResumeState, j.StopReason = "blocked", "publishing", "remote base changed; refusing publication"
	j.Evidence = CheckEvidence{Passed: true, Head: testHead, Tree: testTree}
	p := &unpublishedTestPublisher{resumeTestPublisher: &resumeTestPublisher{branchSHA: testTree}}
	return dir, j, p, &resumeTestRepository{}
}
func TestUnpublishedRebaseInvalidatesEvidence(t *testing.T) {
	dir, j, p, repo := unpublishedFixture(t)
	j.SessionID = "original-session"
	changed, err := (Runner{Publisher: p, Repository: repo, Rebase: true}).checkResumeUpstream(context.Background(), dir, j)
	if err != nil || !changed {
		t.Fatalf("rebase: %v %v", changed, err)
	}
	if j.Plan.BaseSHA != testTree || j.Evidence.Passed || j.State != "implementing" || !j.RestackNeedsChecks || j.SessionID != "original-session" || repo.calls != 1 || j.Publication.Number != 0 {
		t.Fatalf("invalid state: %+v", j)
	}
}
func TestUnpublishedRebaseRecoversCheckpoint(t *testing.T) {
	dir, j, p, repo := unpublishedFixture(t)
	repo.fail = true
	r := Runner{Publisher: p, Repository: repo, Rebase: true}
	if _, err := r.checkResumeUpstream(context.Background(), dir, j); err == nil {
		t.Fatal("expected interruption")
	}
	saved, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	repo.fail = false
	r.Rebase = false
	if changed, err := r.checkResumeUpstream(context.Background(), dir, &saved); err != nil || !changed {
		t.Fatalf("recovery: %v %v", changed, err)
	}
	if p.snapshots != 1 || saved.Evidence.Passed {
		t.Fatal("recovery replaced snapshot or retained evidence")
	}
}
func TestUnpublishedRebaseRejectsUnsafeDestinationAndDirtyCandidate(t *testing.T) {
	for _, tc := range []string{"remote", "dirty", "different hold"} {
		t.Run(tc, func(t *testing.T) {
			dir, j, p, repo := unpublishedFixture(t)
			switch tc {
			case "remote":
				p.unsafe = true
			case "dirty":
				repo.dirty = true
			case "different hold":
				j.ResumeState = "implementing"
			}
			if _, err := (Runner{Publisher: p, Repository: repo, Rebase: true}).checkResumeUpstream(context.Background(), dir, j); err == nil {
				t.Fatal("unsafe rebase accepted")
			}
			if repo.calls != 0 || p.snapshots != 0 {
				t.Fatal("unsafe rebase changed workspace")
			}
		})
	}
}
func TestUnpublishedRebaseRejectsMovedTargetDuringRecovery(t *testing.T) {
	dir, j, p, repo := unpublishedFixture(t)
	repo.fail = true
	r := Runner{Publisher: p, Repository: repo, Rebase: true}
	_, _ = r.checkResumeUpstream(context.Background(), dir, j)
	p.branchSHA = testHead
	repo.fail = false
	if _, err := r.checkResumeUpstream(context.Background(), dir, j); err == nil || !strings.Contains(err.Error(), "base moved") {
		t.Fatalf("expected moved base error, got %v", err)
	}
}

func TestVerifyUnpublishedChecksRemoteBranchAndPR(t *testing.T) {
	for _, scenario := range []string{"clear", "branch", "pr", "incomplete", "null"} {
		t.Run(scenario, func(t *testing.T) {
			p := GitHubPublisher{Frozen: frozenTestIdentity(), Command: func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name == "git" {
					return nil, nil
				}
				if len(args) < 2 {
					return nil, fmt.Errorf("unexpected command")
				}
				switch {
				case args[1] == "user":
					return []byte(`{"id":123,"login":"example"}`), nil
				case args[1] == "repos/example/project":
					return []byte(`{"id":99,"full_name":"example/project"}`), nil
				case strings.Contains(args[1], "/git/matching-refs/"):
					if scenario == "null" {
						return []byte(`null`), nil
					}
					if scenario == "incomplete" {
						return nil, fmt.Errorf("offline")
					}
					if scenario == "branch" {
						return []byte(`[{"ref":"refs/heads/task"}]`), nil
					}
					return []byte(`[]`), nil
				case strings.Contains(args[1], "/pulls?"):
					if scenario == "pr" {
						return []byte(`[{}]`), nil
					}
					return []byte(`[]`), nil
				default:
					return nil, fmt.Errorf("unexpected endpoint")
				}
			}}
			err := p.VerifyUnpublished(context.Background(), Plan{Repository: "example/project", Branch: "task"})
			if (err == nil) != (scenario == "clear") {
				t.Fatalf("unexpected result: %v", err)
			}
		})
	}
}
