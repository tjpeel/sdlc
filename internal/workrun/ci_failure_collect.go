package workrun

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

var ciAppSlug = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,99}$`)
var ciSeverity = regexp.MustCompile(`(?i)error|exception|fail|fatal|panic`)

type ciRequestedCheck struct {
	Name   string `json:"name"`
	Bucket string `json:"bucket"`
	State  string `json:"state"`
	Link   string `json:"link"`
}
type ciCommitStatus struct {
	ID          int64  `json:"id"`
	Context     string `json:"context"`
	State       string `json:"state"`
	Description string `json:"description"`
	TargetURL   string `json:"target_url"`
	Creator     struct {
		ID int64 `json:"id"`
	} `json:"creator"`
}
type ciActionStep struct {
	Name        string `json:"name"`
	Number      int    `json:"number"`
	Status      string `json:"status"`
	Conclusion  string `json:"conclusion"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at"`
}
type ciActionJob struct {
	ID          int64          `json:"id"`
	RunID       int64          `json:"run_id"`
	Head        string         `json:"head_sha"`
	Name        string         `json:"name"`
	Status      string         `json:"status"`
	Conclusion  string         `json:"conclusion"`
	CheckRunURL string         `json:"check_run_url"`
	Steps       []ciActionStep `json:"steps"`
}
type ciActionRun struct {
	ID         int64  `json:"id"`
	Attempt    int    `json:"run_attempt"`
	Head       string `json:"head_sha"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Repository struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	} `json:"repository"`
}

func (p GitHubPublisher) ciCheckRuns(ctx context.Context, repo, head string) ([]ciCheckRun, error) {
	var runs []ciCheckRun
	total := -1
	seen := map[int64]bool{}
	for page := 1; page <= 2; page++ {
		var listing struct {
			Total int          `json:"total_count"`
			Runs  []ciCheckRun `json:"check_runs"`
		}
		if err := p.ciAPI(ctx, fmt.Sprintf("repos/%s/commits/%s/check-runs?per_page=100&page=%d", repo, head, page), &listing); err != nil {
			return nil, err
		}
		if listing.Total < 0 || listing.Total > 200 || listing.Runs == nil || len(listing.Runs) > 100 || total >= 0 && total != listing.Total {
			return nil, fmt.Errorf("invalid CI check-run listing")
		}
		total = listing.Total
		for _, run := range listing.Runs {
			if run.ID < 1 || seen[run.ID] || run.Head != head || !validCICheckStatus(run.Status) {
				return nil, fmt.Errorf("invalid CI check-run identity")
			}
			seen[run.ID] = true
			runs = append(runs, run)
		}
		if len(runs) == total {
			return runs, nil
		}
		if len(listing.Runs) != 100 {
			return nil, fmt.Errorf("incomplete CI check-run listing")
		}
	}
	return nil, fmt.Errorf("incomplete CI check-run listing")
}

func (p GitHubPublisher) ciCommitStatuses(ctx context.Context, repo, head string) ([]ciCommitStatus, error) {
	var statuses []ciCommitStatus
	total := -1
	seen := map[int64]bool{}
	for page := 1; page <= 2; page++ {
		var listing struct {
			SHA      string           `json:"sha"`
			Total    int              `json:"total_count"`
			Statuses []ciCommitStatus `json:"statuses"`
		}
		if err := p.ciAPI(ctx, fmt.Sprintf("repos/%s/commits/%s/status?per_page=100&page=%d", repo, head, page), &listing); err != nil {
			return nil, err
		}
		if listing.SHA != head || listing.Total < 0 || listing.Total > 200 || listing.Statuses == nil || len(listing.Statuses) > 100 || total >= 0 && total != listing.Total {
			return nil, fmt.Errorf("invalid committed CI status listing")
		}
		total = listing.Total
		for _, s := range listing.Statuses {
			if s.ID < 1 || seen[s.ID] || s.Creator.ID < 1 {
				return nil, fmt.Errorf("invalid committed CI status identity")
			}
			seen[s.ID] = true
			statuses = append(statuses, s)
		}
		if len(statuses) == total {
			return statuses, nil
		}
		if len(listing.Statuses) != 100 {
			return nil, fmt.Errorf("incomplete committed CI status listing")
		}
	}
	return nil, fmt.Errorf("incomplete committed CI status listing")
}

func (p GitHubPublisher) collectCIFailureContext(ctx context.Context, plan Plan, pr Publication, requested []ciRequestedCheck, diagnostics []CIAnnotation) (*CIFailureContext, error) {
	failed := []ciRequestedCheck{}
	for _, c := range requested {
		if c.Bucket == "fail" || c.Bucket == "cancel" || c.Bucket == "skipping" {
			failed = append(failed, c)
		}
	}
	if len(failed) == 0 {
		return nil, nil
	}
	if len(failed) > maxCIFailedChecks {
		return nil, fmt.Errorf("failed CI check count exceeds collection bound")
	}
	runs, err := p.ciCheckRuns(ctx, plan.Repository, pr.HeadSHA)
	if err != nil {
		return nil, err
	}
	context := &CIFailureContext{Head: pr.HeadSHA}
	seenNames := map[string]bool{}
	var statuses []ciCommitStatus
	statusesLoaded := false
	for _, wanted := range failed {
		if seenNames[wanted.Name] {
			return nil, fmt.Errorf("ambiguous failed required CI identity")
		}
		seenNames[wanted.Name] = true
		matches := []ciCheckRun{}
		for _, r := range runs {
			if r.Name == wanted.Name && r.Status == "completed" && (failedCIConclusion(r.Conclusion) || r.Conclusion == "skipped") {
				matches = append(matches, r)
			}
		}
		if len(matches) > 1 {
			return nil, fmt.Errorf("ambiguous failed CI check-run")
		}
		if len(matches) == 1 {
			listed := matches[0]
			if listed.App.ID < 1 || !ciAppSlug.MatchString(listed.App.Slug) || wanted.Link == "" || (wanted.Link != listed.DetailsURL && wanted.Link != listed.HTMLURL) {
				return nil, fmt.Errorf("failed CI link or app identity is unverified")
			}
			if (listed.App.Slug == "github-actions") != (listed.App.ID == 15368) {
				return nil, fmt.Errorf("GitHub Actions app identity is unverified")
			}
			var run ciCheckRun
			if err := p.ciAPI(ctx, fmt.Sprintf("repos/%s/check-runs/%d", plan.Repository, listed.ID), &run); err != nil {
				return nil, err
			}
			if run != listed {
				return nil, fmt.Errorf("CI check-run changed before failure collection")
			}
			if run.Output.Count < 0 || run.Output.Count > 200 {
				return nil, fmt.Errorf("CI annotation count exceeds collection bound")
			}
			text := run.Output.Title + "\n" + run.Output.Summary + "\n" + run.Output.Text
			text = strings.TrimSpace(text)
			isNativeSonar := run.App.Slug == "sonarqubecloud" || run.App.Slug == "sonarcloud" || run.App.Slug == "sonarqube"
			if !isNativeSonar {
				for page, count := 1, 0; count < run.Output.Count; page++ {
					var annotations []struct {
						Path    string `json:"path"`
						Line    int    `json:"start_line"`
						Level   string `json:"annotation_level"`
						Title   string `json:"title"`
						Message string `json:"message"`
					}
					if err := p.ciAPI(ctx, fmt.Sprintf("repos/%s/check-runs/%d/annotations?per_page=100&page=%d", plan.Repository, run.ID, page), &annotations); err != nil {
						return nil, err
					}
					if annotations == nil || len(annotations) > 100 || len(annotations) == 0 || count+len(annotations) > run.Output.Count {
						return nil, fmt.Errorf("incomplete generic CI annotations")
					}
					count += len(annotations)
					for _, a := range annotations {
						text += fmt.Sprintf("\nAnnotation %s:%d (%s): %s\n%s", a.Path, a.Line, a.Level, a.Title, a.Message)
						if len(text) > maxCIAPIBytes {
							return nil, fmt.Errorf("CI selected annotation payload exceeds bound")
						}
					}
					if count < run.Output.Count && len(annotations) != 100 {
						return nil, fmt.Errorf("incomplete generic CI annotation page")
					}
				}
			}
			if run.App.ID == 15368 && run.App.Slug == "github-actions" {
				entry, err := p.collectCIActionFailure(ctx, plan, pr, run, text)
				if err != nil {
					return nil, err
				}
				context.Checks = append(context.Checks, entry)
			} else {
				if isNativeSonar && len(diagnostics) > 0 {
					data, _ := json.Marshal(diagnostics)
					text += "\nController normalized source diagnostics:\n" + string(data)
				}
				entry := CIFailedCheck{Kind: "check", CheckID: run.ID, AppID: run.App.ID, AppSlug: run.App.Slug, Conclusion: run.Conclusion}
				if err := p.sanitizeCIFailedCheck(&entry, run.Name, text, false); err != nil {
					return nil, err
				}
				context.Checks = append(context.Checks, entry)
			}
			var after ciCheckRun
			if err := p.ciAPI(ctx, fmt.Sprintf("repos/%s/check-runs/%d", plan.Repository, run.ID), &after); err != nil {
				return nil, err
			}
			if after != run {
				return nil, fmt.Errorf("CI check-run changed during failure collection")
			}
			continue
		}
		if !statusesLoaded {
			statuses, err = p.ciCommitStatuses(ctx, plan.Repository, pr.HeadSHA)
			if err != nil {
				return nil, err
			}
			statusesLoaded = true
		}
		matched := []ciCommitStatus{}
		for _, s := range statuses {
			if s.Context == wanted.Name && (s.State == "failure" || s.State == "error") && s.TargetURL == wanted.Link {
				matched = append(matched, s)
			}
		}
		if len(matched) != 1 {
			return nil, fmt.Errorf("failed CI status could not be identified uniquely")
		}
		s := matched[0]
		entry := CIFailedCheck{Kind: "status", StatusID: s.ID, Conclusion: s.State}
		if err := p.sanitizeCIFailedCheck(&entry, s.Context, s.Description, false); err != nil {
			return nil, err
		}
		context.Checks = append(context.Checks, entry)
		latest, err := p.ciCommitStatuses(ctx, plan.Repository, pr.HeadSHA)
		if err != nil {
			return nil, err
		}
		found := 0
		for _, current := range latest {
			if current.Context == s.Context {
				if current != s {
					return nil, fmt.Errorf("committed CI status changed")
				}
				found++
			}
		}
		if found != 1 {
			return nil, fmt.Errorf("committed CI status changed")
		}
	}
	sort.Slice(context.Checks, func(i, j int) bool {
		a, b := context.Checks[i], context.Checks[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.CheckID != b.CheckID {
			return a.CheckID < b.CheckID
		}
		return a.StatusID < b.StatusID
	})
	if err := fitCIFailureContext(context); err != nil {
		return nil, err
	}
	if err := validateCIFailureContext(CIResult{Status: "failed", FailureContext: context}, pr); err != nil {
		return nil, err
	}
	relevant := false
	for _, entry := range context.Checks {
		relevant = relevant || strings.TrimSpace(entry.Text) != ""
	}
	if !relevant {
		return nil, fmt.Errorf("failed CI supplied no relevant failure evidence")
	}
	return context, nil
}

func (p GitHubPublisher) sanitizeCIFailedCheck(entry *CIFailedCheck, name, text string, truncated bool) error {
	cleanSlug, _, err := sanitizeCIText(entry.AppSlug, p.CIPrivateValues)
	if err != nil || cleanSlug != entry.AppSlug {
		return fmt.Errorf("CI app identity conflicts with private masking values")
	}
	cleanName, r1, err := sanitizeCIText(name, p.CIPrivateValues)
	if err != nil {
		return err
	}
	clean, r2, err := sanitizeCIText(text, p.CIPrivateValues)
	if err != nil {
		return err
	}
	entry.Name, entry.Truncated = ciTextExcerpt(cleanName, 240)
	entry.Text, entry.Redacted = clean, r1 || r2
	var textTruncated bool
	entry.Text, textTruncated = ciActionLogExcerpt(entry.Text, nil, maxCIFailureText)
	entry.Truncated = entry.Truncated || textTruncated || truncated
	return nil
}

func ciActionIDs(raw, repo string) (int64, int64, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return 0, 0, fmt.Errorf("Actions check details URL is unverified")
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	prefix := strings.Split(repo, "/")
	if len(parts) != 7 || parts[0] != prefix[0] || parts[1] != prefix[1] || parts[2] != "actions" || parts[3] != "runs" || parts[5] != "job" {
		return 0, 0, fmt.Errorf("Actions check details URL is unverified")
	}
	run, e1 := strconv.ParseInt(parts[4], 10, 64)
	job, e2 := strconv.ParseInt(parts[6], 10, 64)
	if e1 != nil || e2 != nil || run < 1 || job < 1 || fmt.Sprint(run) != parts[4] || fmt.Sprint(job) != parts[6] {
		return 0, 0, fmt.Errorf("Actions check details identity is invalid")
	}
	return run, job, nil
}

func (p GitHubPublisher) actionAttemptJob(ctx context.Context, repo string, run ciActionRun, expected ciActionJob) error {
	seen := map[int64]bool{}
	total := -1
	found := 0
	for page := 1; page <= 2; page++ {
		var listing struct {
			Total int           `json:"total_count"`
			Jobs  []ciActionJob `json:"jobs"`
		}
		if err := p.ciAPI(ctx, fmt.Sprintf("repos/%s/actions/runs/%d/attempts/%d/jobs?per_page=100&page=%d", repo, run.ID, run.Attempt, page), &listing); err != nil {
			return err
		}
		if listing.Total < 1 || listing.Total > 200 || listing.Jobs == nil || len(listing.Jobs) > 100 || total >= 0 && total != listing.Total {
			return fmt.Errorf("invalid Actions attempt job listing")
		}
		total = listing.Total
		for _, job := range listing.Jobs {
			if job.ID < 1 || seen[job.ID] {
				return fmt.Errorf("ambiguous Actions attempt job identity")
			}
			seen[job.ID] = true
			if job.ID == expected.ID {
				if !reflect.DeepEqual(job, expected) {
					return fmt.Errorf("Actions job does not match current attempt")
				}
				found++
			}
		}
		if len(seen) == total {
			if found != 1 {
				return fmt.Errorf("Actions job does not belong to current run attempt")
			}
			return nil
		}
		if len(listing.Jobs) != 100 {
			return fmt.Errorf("incomplete Actions attempt job listing")
		}
	}
	return fmt.Errorf("incomplete Actions attempt job listing")
}

func (p GitHubPublisher) collectCIActionFailure(ctx context.Context, plan Plan, pr Publication, check ciCheckRun, text string) (CIFailedCheck, error) {
	fail := func(message string) (CIFailedCheck, error) { return CIFailedCheck{}, fmt.Errorf("%s", message) }
	runID, jobID, err := ciActionIDs(check.DetailsURL, plan.Repository)
	if err != nil {
		return CIFailedCheck{}, err
	}
	var run ciActionRun
	var job ciActionJob
	if err := p.ciAPI(ctx, fmt.Sprintf("repos/%s/actions/runs/%d", plan.Repository, runID), &run); err != nil {
		return CIFailedCheck{}, err
	}
	if run.ID != runID || run.Head != pr.HeadSHA || run.Attempt < 1 || run.Repository.ID < 1 || run.Repository.FullName != plan.Repository || !validCICheckStatus(run.Status) || p.Frozen != nil && run.Repository.ID != p.Frozen.RepositoryID {
		return fail("Actions run identity changed")
	}
	var repository struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
	}
	if err := p.ciAPI(ctx, "repos/"+plan.Repository, &repository); err != nil {
		return CIFailedCheck{}, err
	}
	if repository.ID != run.Repository.ID || repository.FullName != plan.Repository {
		return fail("Actions repository identity changed")
	}
	if err := p.ciAPI(ctx, fmt.Sprintf("repos/%s/actions/jobs/%d", plan.Repository, jobID), &job); err != nil {
		return CIFailedCheck{}, err
	}
	if job.ID != jobID || job.RunID != runID || job.Head != pr.HeadSHA || job.Name != check.Name || job.Status != "completed" || job.Conclusion != check.Conclusion || job.CheckRunURL != fmt.Sprintf("https://api.github.com/repos/%s/check-runs/%d", plan.Repository, check.ID) || len(job.Steps) > 100 {
		return fail("Actions job identity changed")
	}
	if err := p.actionAttemptJob(ctx, plan.Repository, run, job); err != nil {
		return CIFailedCheck{}, err
	}
	stepNumbers := map[int]bool{}
	for _, step := range job.Steps {
		if step.Number < 1 || step.Number > 100 || stepNumbers[step.Number] || step.Name == "" || len(step.Name) > 4096 || !validCICheckStatus(step.Status) {
			return fail("invalid Actions step metadata")
		}
		stepNumbers[step.Number] = true
		for _, timestamp := range []string{step.StartedAt, step.CompletedAt} {
			if timestamp != "" {
				if _, err := time.Parse(time.RFC3339Nano, timestamp); err != nil {
					return fail("invalid Actions step timestamp")
				}
			}
		}
		if step.Status == "completed" && failedCIConclusion(step.Conclusion) {
			text += fmt.Sprintf("\nFailed step %d (%s): %s", step.Number, step.Conclusion, step.Name)
		}
	}
	data, err := p.command(ctx, "gh", "api", "--hostname", "github.com", "--allow-escape-sequences", fmt.Sprintf("repos/%s/actions/jobs/%d/logs", plan.Repository, jobID))
	if err != nil || len(data) > maxCIAPIBytes {
		return fail("Actions job log API could not be read within its bound")
	}
	clean, redacted, err := sanitizeCIText(string(data), p.CIPrivateValues)
	if err != nil {
		return CIFailedCheck{}, err
	}
	excerpt, truncated := ciActionLogExcerpt(clean, job.Steps)
	if strings.TrimSpace(excerpt) != "" {
		text += "\nActions job log evidence:\n" + excerpt
	}
	var afterRun ciActionRun
	var afterJob ciActionJob
	if err := p.ciAPI(ctx, fmt.Sprintf("repos/%s/actions/runs/%d", plan.Repository, runID), &afterRun); err != nil {
		return CIFailedCheck{}, err
	}
	if err := p.ciAPI(ctx, fmt.Sprintf("repos/%s/actions/jobs/%d", plan.Repository, jobID), &afterJob); err != nil {
		return CIFailedCheck{}, err
	}
	if run != afterRun || !reflect.DeepEqual(job, afterJob) {
		return fail("Actions run or job changed during log collection")
	}
	if err := p.actionAttemptJob(ctx, plan.Repository, run, job); err != nil {
		return CIFailedCheck{}, err
	}
	entry := CIFailedCheck{Kind: "actions", CheckID: check.ID, AppID: check.App.ID, AppSlug: check.App.Slug, RunID: runID, RunAttempt: run.Attempt, JobID: jobID, RepositoryID: run.Repository.ID, Conclusion: job.Conclusion}
	if err := p.sanitizeCIFailedCheck(&entry, check.Name, text, truncated); err != nil {
		return CIFailedCheck{}, err
	}
	entry.Redacted = entry.Redacted || redacted
	return entry, nil
}

func ciActionLogExcerpt(clean string, steps []ciActionStep, limits ...int) (string, bool) {
	limit := maxCIJobExcerpt
	if len(limits) > 0 {
		limit = limits[0]
	}
	if len(clean) <= limit {
		return clean, false
	}
	lines := strings.Split(clean, "\n")
	severity, insideSeverity, window := []int{}, []int{}, []int{}
	for i, line := range lines {
		inWindow := false
		if token, _, ok := strings.Cut(line, " "); ok {
			if timestamp, err := time.Parse(time.RFC3339Nano, token); err == nil {
				for _, step := range steps {
					if failedCIConclusion(step.Conclusion) {
						start, e1 := time.Parse(time.RFC3339Nano, step.StartedAt)
						end, e2 := time.Parse(time.RFC3339Nano, step.CompletedAt)
						if e1 == nil && e2 == nil && !timestamp.Before(start) && !timestamp.After(end) {
							inWindow = true
						}
					}
				}
			}
		}
		if inWindow {
			window = append(window, i)
		}
		if ciSeverity.MatchString(line) {
			severity = append(severity, i)
			if inWindow {
				insideSeverity = append(insideSeverity, i)
			}
		}
	}
	selected := insideSeverity
	if len(selected) == 0 {
		selected = severity
	}
	if len(selected) == 0 && len(window) > 0 {
		selected = []int{window[0]}
		if window[len(window)-1] != window[0] {
			selected = append(selected, window[len(window)-1])
		}
	}
	// Cover multiple error anchors rather than only the first/last summaries.
	// Long logs retain deterministic coverage of up to 64 selected lines.
	if len(selected) > 64 {
		sample := make([]int, 64)
		for i := range sample {
			sample[i] = selected[i*(len(selected)-1)/(len(sample)-1)]
		}
		selected = sample
	}
	parts := []string{}
	anchorBudget := limit * 3 / 4
	if len(selected) > 0 {
		perAnchor := anchorBudget/len(selected) - 1
		if perAnchor < 1 {
			perAnchor = 1
		}
		anchors := []string{}
		for _, index := range selected {
			line := lines[index]
			if token, body, ok := strings.Cut(line, " "); ok {
				if _, err := time.Parse(time.RFC3339Nano, token); err == nil {
					line = body
				}
			}
			anchor, _ := ciTextExcerpt(line, perAnchor)
			anchors = append(anchors, anchor)
		}
		parts = append(parts, "Selected failure evidence lines:\n"+strings.Join(anchors, "\n"))
	}
	tailBudget := limit - anchorBudget
	if len(selected) == 0 {
		tailBudget = limit
	}
	if len(window) > 0 {
		stepHeadTail, _ := ciTextExcerpt(strings.Join(lines[window[0]:window[len(window)-1]+1], "\n"), tailBudget/2)
		parts = append(parts, "Failed-step boundary context:\n"+stepHeadTail)
		tailBudget /= 2
	}
	headTail, _ := ciTextExcerpt(clean, tailBudget)
	parts = append(parts, headTail)
	excerpt, _ := ciTextExcerpt(strings.Join(parts, "\n[other log regions omitted]\n"), limit)
	return excerpt, true
}

func fitCIFailureContext(c *CIFailureContext) error {
	original := make([]string, len(c.Checks))
	for i := range c.Checks {
		original[i] = c.Checks[i].Text
	}
	for limit := maxCIFailureText; limit >= 128; limit /= 2 {
		data, err := json.Marshal(c)
		if err != nil {
			return fmt.Errorf("CI context could not be encoded")
		}
		if len(data) <= maxCIFailureContextBytes {
			return nil
		}
		for i := range c.Checks {
			var truncated bool
			c.Checks[i].Text, truncated = ciActionLogExcerpt(original[i], nil, limit/2)
			c.Checks[i].Truncated = c.Checks[i].Truncated || truncated
		}
	}
	return fmt.Errorf("CI failure metadata exceeds aggregate context bound")
}
