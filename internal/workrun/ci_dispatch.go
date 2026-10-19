package workrun

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/project"
)

type CIWorkflowDispatch struct {
	Publication Publication       `json:"publication"`
	Workflow    string            `json:"workflow"`
	WorkflowID  int64             `json:"workflow_id"`
	Ref         string            `json:"ref"`
	RequestedAt time.Time         `json:"requested_at"`
	Inputs      map[string]string `json:"inputs,omitempty"`
	Attempted   bool              `json:"attempted,omitempty"`
	Submitted   bool              `json:"submitted,omitempty"`
	RunID       int64             `json:"run_id,omitempty"`
	URL         string            `json:"url,omitempty"`
}

// WorkflowPreparation records an observed controller preflight finding, not a
// claim that GitHub executed or failed a job.
type CIWorkflowPreparation struct {
	Head string `json:"head"`
	Path string `json:"path"`
	Code string `json:"code"`
}
type workflowContextError struct{}

func (*workflowContextError) Error() string {
	return "manual PR workflow references pull_request context without declaring pull_request_number, base_sha and head_sha inputs"
}

type CIWorkflowPublisher interface {
	PrepareCIWorkflow(context.Context, Plan, Publication, string, string) (*CIWorkflowDispatch, error)
	DispatchCIWorkflow(context.Context, Plan, CIWorkflowDispatch) error
	ReadCIWorkflow(context.Context, Plan, CIWorkflowDispatch) (CIWorkflowDispatch, string, error)
}

func (p GitHubPublisher) ciDispatchJSON(ctx context.Context, endpoint string, target any) error {
	data, err := readCIStatus(ctx, func() ([]byte, error) { return p.command(ctx, "gh", "api", "--hostname", "github.com", endpoint) }, waitCIJobLog)
	if err != nil {
		return err
	}
	if len(data) > maxCIAPIBytes || json.Unmarshal(data, target) != nil {
		return fmt.Errorf("invalid workflow API response")
	}
	return nil
}

func workflowDispatchInputs(data []byte, values map[string]string) (map[string]string, error) {
	spec, err := readWorkflowDispatchDeclaration(data)
	if err != nil {
		return nil, err
	}
	if strings.Contains(string(data), "github.event.pull_request.base.sha || github.sha") || strings.Contains(string(data), "github.event.pull_request.head.sha || github.sha") {
		for _, key := range []string{"pull_request_number", "base_sha", "head_sha"} {
			if _, ok := spec[key]; !ok {
				return nil, &workflowContextError{}
			}
		}
	}
	inputs := map[string]string{}
	for name, input := range spec {
		if value, ok := values[name]; ok {
			inputs[name] = value
		} else if input.Required && input.Default == "" {
			return nil, fmt.Errorf("configured workflow has an unsupported required input")
		}
	}
	return inputs, nil
}

func (p GitHubPublisher) PrepareCIWorkflow(ctx context.Context, plan Plan, pr Publication, workflow, runID string) (*CIWorkflowDispatch, error) {
	if err := project.ValidateCIWorkflow(workflow); err != nil || workflow == "" {
		return nil, fmt.Errorf("invalid configured CI workflow")
	}
	if err := p.feedbackBoundary(ctx, plan, pr); err != nil {
		return nil, err
	}
	var repo struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := p.ciDispatchJSON(ctx, "repos/"+plan.Repository, &repo); err != nil {
		return nil, err
	}
	if repo.DefaultBranch == "" {
		return nil, fmt.Errorf("repository default branch unavailable")
	}
	if plan.Base == repo.DefaultBranch {
		return nil, nil
	}
	var meta struct {
		ID          int64
		Path, State string
	}
	if err := p.ciDispatchJSON(ctx, "repos/"+plan.Repository+"/actions/workflows/"+url.PathEscape(workflow), &meta); err != nil {
		return nil, err
	}
	if meta.ID < 1 || meta.State != "active" || meta.Path != ".github/workflows/"+workflow {
		return nil, fmt.Errorf("configured workflow is missing, inactive or has a different path")
	}
	var contents struct{ Content, Encoding string }
	endpoint := "repos/" + plan.Repository + "/contents/" + meta.Path + "?ref=" + pr.HeadSHA
	if err := p.ciDispatchJSON(ctx, endpoint, &contents); err != nil {
		return nil, err
	}
	if contents.Encoding != "base64" {
		return nil, fmt.Errorf("workflow contents have unsupported encoding")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(contents.Content, "\n", ""))
	if err != nil {
		return nil, fmt.Errorf("invalid workflow contents")
	}
	inputs, err := workflowDispatchInputs(raw, map[string]string{"pull_request_number": strconv.Itoa(pr.Number), "base_ref": plan.Base, "base_sha": pr.BaseSHA, "head_sha": pr.HeadSHA, "sdlc_dispatch_id": "sdlc-" + runID + "-" + pr.HeadSHA[:12]})
	if err != nil {
		return nil, err
	}
	return &CIWorkflowDispatch{Publication: pr, Workflow: workflow, WorkflowID: meta.ID, Ref: plan.Branch, RequestedAt: time.Now().UTC(), Inputs: inputs}, nil
}

func (p GitHubPublisher) DispatchCIWorkflow(ctx context.Context, plan Plan, d CIWorkflowDispatch) error {
	if err := p.feedbackBoundary(ctx, plan, d.Publication); err != nil {
		return err
	}
	args := []string{"api", "--hostname", "github.com", "--method", "POST", "repos/" + plan.Repository + "/actions/workflows/" + strconv.FormatInt(d.WorkflowID, 10) + "/dispatches", "--raw-field", "ref=" + d.Ref}
	for name, value := range d.Inputs {
		args = append(args, "--raw-field", "inputs["+name+"]="+value)
	}
	// A dispatch is a side effect. Never retry a POST with an uncertain outcome.
	_, err := p.command(ctx, "gh", args...)
	return err
}

type dispatchedWorkflowRun struct {
	ID                        int64
	WorkflowID                int64  `json:"workflow_id"`
	HeadSHA                   string `json:"head_sha"`
	HeadBranch                string `json:"head_branch"`
	Event, Status, Conclusion string
	CreatedAt                 time.Time `json:"created_at"`
	HTMLURL                   string    `json:"html_url"`
	DisplayTitle              string    `json:"display_title"`
}

func (p GitHubPublisher) ReadCIWorkflow(ctx context.Context, plan Plan, d CIWorkflowDispatch) (CIWorkflowDispatch, string, error) {
	if err := p.feedbackBoundary(ctx, plan, d.Publication); err != nil {
		return d, "", err
	}
	var run dispatchedWorkflowRun
	if d.RunID != 0 {
		if err := p.ciDispatchJSON(ctx, "repos/"+plan.Repository+"/actions/runs/"+strconv.FormatInt(d.RunID, 10), &run); err != nil {
			return d, "", err
		}
	} else {
		var runs struct {
			WorkflowRuns []dispatchedWorkflowRun `json:"workflow_runs"`
		}
		endpoint := "repos/" + plan.Repository + "/actions/workflows/" + strconv.FormatInt(d.WorkflowID, 10) + "/runs?event=workflow_dispatch&head_sha=" + d.Publication.HeadSHA + "&per_page=100"
		if err := p.ciDispatchJSON(ctx, endpoint, &runs); err != nil {
			return d, "", err
		}
		for _, candidate := range runs.WorkflowRuns {
			if candidate.HeadSHA != d.Publication.HeadSHA || candidate.WorkflowID != d.WorkflowID || candidate.Event != "workflow_dispatch" || candidate.HeadBranch != d.Ref || candidate.CreatedAt.Before(d.RequestedAt.Add(-2*time.Second)) {
				continue
			}
			if id := d.Inputs["sdlc_dispatch_id"]; id != "" && !strings.Contains(candidate.DisplayTitle, id) {
				continue
			}
			if run.ID != 0 {
				return d, "", fmt.Errorf("multiple workflow runs match the saved dispatch; manual reconciliation required")
			}
			run = candidate
		}
		if run.ID == 0 {
			return d, "pending", nil
		}
	}
	if run.ID < 1 || run.WorkflowID != d.WorkflowID || run.HeadSHA != d.Publication.HeadSHA || run.HeadBranch != d.Ref || run.Event != "workflow_dispatch" || run.HTMLURL != fmt.Sprintf("https://github.com/%s/actions/runs/%d", plan.Repository, run.ID) {
		return d, "", fmt.Errorf("dispatched workflow run differs from the frozen publication")
	}
	d.RunID, d.URL = run.ID, run.HTMLURL
	if run.Status != "completed" {
		return d, "pending", nil
	}
	if run.Conclusion == "success" {
		return d, "passed", nil
	}
	return d, "failed", nil
}

func (p DockerPublisher) PrepareCIWorkflow(ctx context.Context, plan Plan, pr Publication, workflow, runID string) (d *CIWorkflowDispatch, err error) {
	err = p.withFeedbackPublisher(ctx, plan, func(g GitHubPublisher) error {
		var e error
		d, e = g.PrepareCIWorkflow(ctx, plan, pr, workflow, runID)
		return e
	})
	return
}
func (p DockerPublisher) DispatchCIWorkflow(ctx context.Context, plan Plan, d CIWorkflowDispatch) error {
	return p.withFeedbackPublisher(ctx, plan, func(g GitHubPublisher) error { return g.DispatchCIWorkflow(ctx, plan, d) })
}
func (p DockerPublisher) ReadCIWorkflow(ctx context.Context, plan Plan, d CIWorkflowDispatch) (next CIWorkflowDispatch, status string, err error) {
	err = p.withFeedbackPublisher(ctx, plan, func(g GitHubPublisher) error { var e error; next, status, e = g.ReadCIWorkflow(ctx, plan, d); return e })
	return
}

func (r Runner) ensureCIWorkflow(ctx context.Context, dir string, j *Journal, checks CIResult) (CIResult, error) {
	workflow := j.Plan.NonDefaultCIWorkflow
	if r.NonDefaultCIWorkflow != nil {
		workflow = *r.NonDefaultCIWorkflow
	}
	var current *CIWorkflowDispatch
	index := -1
	for i := range j.CIWorkflowDispatches {
		d := &j.CIWorkflowDispatches[i]
		if d.Publication == j.Publication {
			if current != nil {
				return checks, fmt.Errorf("duplicate saved workflow dispatch")
			}
			current = d
			index = i
		}
	}
	if current == nil && (workflow == "" || checks.Status != "missing") {
		return checks, nil
	}
	p, ok := r.Publisher.(CIWorkflowPublisher)
	if !ok {
		return checks, fmt.Errorf("publisher cannot dispatch configured CI workflow")
	}
	if current == nil {
		d, err := p.PrepareCIWorkflow(ctx, j.Plan, j.Publication, workflow, j.ID)
		if contextErr, isolated := err.(*workflowContextError); isolated {
			return CIResult{Status: "failed", Details: "Controller preflight: " + contextErr.Error(), WorkflowPreparation: &CIWorkflowPreparation{Head: j.Publication.HeadSHA, Path: ".github/workflows/" + workflow, Code: "missing_dispatch_pr_context"}}, nil
		}
		if err != nil {
			return checks, err
		}
		if d == nil {
			return checks, nil
		}
		j.CIWorkflowDispatches = append(j.CIWorkflowDispatches, *d)
		index = len(j.CIWorkflowDispatches) - 1
		current = &j.CIWorkflowDispatches[index]
		if err := r.save(dir, j); err != nil {
			return checks, err
		}
	}
	if current.RequestedAt.IsZero() || current.WorkflowID < 1 || current.Ref != j.Plan.Branch || project.ValidateCIWorkflow(current.Workflow) != nil {
		return checks, fmt.Errorf("invalid saved CI workflow dispatch")
	}
	if !current.Attempted {
		current.Attempted = true
		if err := r.save(dir, j); err != nil {
			return checks, err
		}
		err := p.DispatchCIWorkflow(ctx, j.Plan, *current)
		if err == nil {
			current.Submitted = true
		}
		if saveErr := r.save(dir, j); saveErr != nil {
			return checks, saveErr
		}
		if err != nil {
			return checks, fmt.Errorf("CI workflow dispatch was not confirmed; resume to reconcile without redispatch: %w", err)
		}
		if r.Output != nil {
			fmt.Fprintf(r.Output, "Dispatched %s for non-default-base PR #%d at %s.\n", current.Workflow, j.Publication.Number, j.Publication.HeadSHA)
		}
	}
	next, status, err := p.ReadCIWorkflow(ctx, j.Plan, *current)
	if err != nil {
		return checks, err
	}
	j.CIWorkflowDispatches[index] = next
	if err := r.save(dir, j); err != nil {
		return checks, err
	}
	if next.RunID == 0 && time.Since(next.RequestedAt) > r.MissingCheckGrace {
		return checks, fmt.Errorf("saved workflow dispatch has no matching run; inspect GitHub before manual reconciliation; dispatch will not be repeated")
	}
	if status == "pending" {
		checks = CIResult{Status: "pending"}
	} else if status == "failed" {
		checks.Status = "failed"
		checks.Details = "Dispatched PR validation failed: " + next.URL
	} else if checks.Status == "missing" {
		checks.Details = "Dispatched workflow completed, awaiting its PR check records: " + next.URL
	}
	return checks, nil
}

func validateCIWorkflowJournal(j *Journal) error {
	if err := project.ValidateCIWorkflow(j.Plan.NonDefaultCIWorkflow); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, d := range j.CIWorkflowDispatches {
		key := d.Publication.HeadSHA + ":" + d.Publication.BaseSHA
		if seen[key] || !objectID.MatchString(d.Publication.HeadSHA) || !objectID.MatchString(d.Publication.BaseSHA) || d.Publication.URL != fmt.Sprintf("https://github.com/%s/pull/%d", j.Plan.Repository, d.Publication.Number) || d.Publication.Number < 1 || d.RequestedAt.IsZero() || d.Workflow == "" || d.WorkflowID < 1 || d.RunID < 0 || d.Ref == "" || project.ValidateCIWorkflow(d.Workflow) != nil {
			return fmt.Errorf("invalid saved workflow dispatch")
		}
		seen[key] = true
		if d.RunID > 0 && d.URL != fmt.Sprintf("https://github.com/%s/actions/runs/%d", j.Plan.Repository, d.RunID) {
			return fmt.Errorf("invalid saved workflow run URL")
		}
		expected := map[string]string{"pull_request_number": strconv.Itoa(d.Publication.Number), "head_sha": d.Publication.HeadSHA, "base_sha": d.Publication.BaseSHA, "sdlc_dispatch_id": "sdlc-" + j.ID + "-" + d.Publication.HeadSHA[:12]}
		for name, value := range d.Inputs {
			if name == "base_ref" {
				if value == "" || strings.ContainsAny(value, "\r\n\x00") {
					return fmt.Errorf("invalid dispatch base ref")
				}
				continue
			}
			if expected[name] != value || value == "" {
				return fmt.Errorf("invalid saved workflow input")
			}
		}
	}
	return nil
}
