package workrun

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type dispatchFixture struct {
	rawWorkflow        string
	plan               Plan
	pr                 Publication
	posts              int
	status, head, base string
	runVisible         bool
	postErr            error
}

func (f *dispatchFixture) command(_ context.Context, name string, args ...string) ([]byte, error) {
	if name != "gh" {
		return nil, fmt.Errorf("unexpected client")
	}
	call := strings.Join(args, " ")
	encode := func(v any) ([]byte, error) { b, e := json.Marshal(v); return b, e }
	if strings.HasPrefix(call, "pr view ") {
		return encode(map[string]any{"number": f.pr.Number, "url": f.pr.URL, "state": "OPEN", "headRefName": f.plan.Branch, "headRefOid": f.pr.HeadSHA, "baseRefName": f.plan.Base, "baseRefOid": f.pr.BaseSHA})
	}
	if strings.Contains(call, "--method POST") {
		f.posts++
		if strings.Contains(call, "inputs[") {
			return nil, fmt.Errorf("runner imposed custom workflow inputs")
		}
		return nil, f.postErr
	}
	if strings.HasSuffix(call, "repos/"+f.plan.Repository) {
		return encode(map[string]string{"default_branch": f.base})
	}
	if strings.Contains(call, "/contents/") {
		return encode(map[string]string{"encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(f.workflow()))})
	}
	run := map[string]any{"id": 51, "workflow_id": 7, "head_sha": f.head, "head_branch": f.plan.Branch, "event": "workflow_dispatch", "status": f.status, "conclusion": "success", "created_at": time.Now().UTC().Format(time.RFC3339Nano), "html_url": fmt.Sprintf("https://github.com/%s/actions/runs/51", f.plan.Repository)}
	if strings.Contains(call, "/actions/runs/51") {
		return encode(run)
	}
	if strings.Contains(call, "/runs?") {
		runs := []any{}
		if f.runVisible {
			runs = append(runs, run)
		}
		return encode(map[string]any{"workflow_runs": runs})
	}
	if strings.Contains(call, "/actions/workflows/") {
		return encode(map[string]any{"id": 7, "state": "active", "path": ".github/workflows/check.yml"})
	}
	return nil, fmt.Errorf("unexpected dispatch request: %s", call)
}
func dispatchTestFixture(t *testing.T) (string, *Journal, Runner, *dispatchFixture) {
	dir, j, r, _, _ := feedbackReadyRun(t)
	j.Plan.Base = "feature-parent"
	f := &dispatchFixture{plan: j.Plan, pr: j.Publication, status: "in_progress", head: j.Publication.HeadSHA, base: "main"}
	workflowName := "check.yml"
	r.NonDefaultCIWorkflow = &workflowName
	r.MissingCheckGrace = time.Minute
	r.Publisher = GitHubPublisher{Command: f.command}
	return dir, j, r, f
}
func TestConfiguredStackedWorkflowDispatchesOnceAndTracksExactHead(t *testing.T) {
	dir, j, r, f := dispatchTestFixture(t)
	result, err := r.ensureCIWorkflow(context.Background(), dir, j, CIResult{Status: "missing"})
	if err != nil || f.posts != 1 || result.Status != "pending" || len(j.CIWorkflowDispatches) != 1 {
		t.Fatal(result, err, f.posts)
	}
	// Resume/repeated polls reconcile the original intent, never POST again.
	f.runVisible = true
	result, err = r.ensureCIWorkflow(context.Background(), dir, j, CIResult{Status: "passed"})
	if err != nil || f.posts != 1 || result.Status != "pending" || j.CIWorkflowDispatches[0].RunID != 51 {
		t.Fatal(result, err, f.posts)
	}
	f.status = "completed"
	result, err = r.ensureCIWorkflow(context.Background(), dir, j, CIResult{Status: "missing"})
	if err != nil || result.Status != "missing" {
		t.Fatal("workflow success bypassed missing PR checks", result, err)
	}
	result, err = r.ensureCIWorkflow(context.Background(), dir, j, CIResult{Status: "passed"})
	if err != nil || result.Status != "passed" || f.posts != 1 {
		t.Fatal(result, err, f.posts)
	}
	f.head = testBase
	if _, err = r.ensureCIWorkflow(context.Background(), dir, j, CIResult{Status: "passed"}); err == nil {
		t.Fatal("accepted run on another head")
	}
}
func TestWorkflowDispatchPolicyAndUncertainOutcome(t *testing.T) {
	t.Run("default branch", func(t *testing.T) {
		dir, j, r, f := dispatchTestFixture(t)
		f.base = j.Plan.Base
		result, err := r.ensureCIWorkflow(context.Background(), dir, j, CIResult{Status: "missing"})
		if err != nil || f.posts != 0 || result.Status != "missing" {
			t.Fatal(result, err, f.posts)
		}
	})
	t.Run("not configured", func(t *testing.T) {
		dir, j, r, f := dispatchTestFixture(t)
		r.NonDefaultCIWorkflow = nil
		_, err := r.ensureCIWorkflow(context.Background(), dir, j, CIResult{Status: "missing"})
		if err != nil || f.posts != 0 {
			t.Fatal(err, f.posts)
		}
	})
	t.Run("uncertain POST", func(t *testing.T) {
		dir, j, r, f := dispatchTestFixture(t)
		f.postErr = errors.New("connection lost")
		_, err := r.ensureCIWorkflow(context.Background(), dir, j, CIResult{Status: "missing"})
		if err == nil || !j.CIWorkflowDispatches[0].Attempted {
			t.Fatal("lost dispatch intent")
		}
		f.postErr = nil
		f.runVisible = true
		_, err = r.ensureCIWorkflow(context.Background(), dir, j, CIResult{Status: "missing"})
		if err != nil || f.posts != 1 {
			t.Fatal("redispatched uncertain POST", err, f.posts)
		}
	})
}
func TestWorkflowDispatchRefusesRequiredInputsWithoutSourceRepair(t *testing.T) {
	err := workflowDispatchInputs([]byte("on:\n  pull_request:\n  workflow_dispatch:\n    inputs:\n      pull_request_number:\n        required: true\n"))
	if err == nil || !strings.Contains(err.Error(), "without changing repository source") {
		t.Fatal("required manual inputs were not treated as an operational limitation", err)
	}
	if err := workflowDispatchInputs([]byte("on:\n  pull_request:\n")); err == nil {
		t.Fatal("dispatched workflow without manual trigger")
	}
}

func TestNoChecksCommandResponseIsConfirmedAsMissing(t *testing.T) {
	pr := Publication{URL: "https://github.com/example/project/pull/9", Number: 9, BaseSHA: testBase, HeadSHA: testHead}
	snapshots := 0
	p := GitHubPublisher{Command: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "pr checks") {
			return nil, ciStatusExit(1)
		}
		snapshots++
		return json.Marshal(map[string]any{"baseRefOid": pr.BaseSHA, "headRefOid": pr.HeadSHA, "state": "OPEN", "statusCheckRollup": []any{}})
	}}
	result, err := p.Checks(context.Background(), Plan{Repository: "example/project"}, pr)
	if err != nil || result.Status != "missing" || snapshots != 2 {
		t.Fatal("empty response was not authoritatively confirmed", result, err, snapshots)
	}
}

func TestExistingManualWorkflowContractDoesNotBecomeSourceRepair(t *testing.T) {
	raw := []byte("on:\n  pull_request:\n    branches:\n      - main\n  workflow_dispatch:\njobs:\n  dependencies:\n    with:\n      base-ref: ${{ github.event.pull_request.base.sha || github.sha }}\n      head-ref: ${{ github.event.pull_request.head.sha || github.sha }}\n")
	if err := workflowDispatchInputs(raw); err != nil {
		t.Fatal("existing manual workflow contract became an invented repository prerequisite", err)
	}
}

func (f *dispatchFixture) workflow() string {
	if f.rawWorkflow != "" {
		return f.rawWorkflow
	}
	return "on:\n  pull_request:\n  workflow_dispatch:\n"
}

type preflightChecksPublisher struct{ GitHubPublisher }

func (p preflightChecksPublisher) Checks(context.Context, Plan, Publication) (CIResult, error) {
	return CIResult{Status: "missing"}, nil
}

func TestUnsupportedManualInputsBlockWithoutGrantingSourceRepair(t *testing.T) {
	dir, j, r, f := dispatchTestFixture(t)
	f.rawWorkflow = "on:\n  pull_request:\n  workflow_dispatch:\n    inputs:\n      pull_request_number:\n        required: true\n"
	r.Publisher = preflightChecksPublisher{GitHubPublisher{Command: f.command}}
	j.State = "ci"
	rounds, attempt := j.Rounds, j.Attempt
	err := r.checkCI(context.Background(), dir, j)
	if !errors.Is(err, ErrStopped) || j.State != "blocked" || j.Rounds != rounds || j.Attempt != attempt || f.posts != 0 || j.CI.WorkflowPreparation != nil {
		t.Fatal("operational limitation granted a repository repair", err, j.State)
	}
}
