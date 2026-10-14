package workrun

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxCIAPIBytes = 512 * 1024
const maxActionsJobLogBytes = 16 * 1024 * 1024
const maxCIDiagnostics = 20

// CIAnnotation contains only approved source metadata and fixed repair kinds.
type CIAnnotation struct {
	Head    string `json:"head"`
	Path    string `json:"path"`
	Line    int    `json:"line"`
	EndLine int    `json:"end_line,omitempty"`
	Level   string `json:"level"`
	Kind    string `json:"kind"`
	Rule    string `json:"rule,omitempty"`
	Current int    `json:"current,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

// CIRepairBudget is separate from configured-check and review accounting.
// A nil budget on a published legacy journal requires explicit recovery.
type CIRepairBudget struct {
	Used            int    `json:"used"`
	StartRound      int    `json:"start_round"`
	LastGrantedHead string `json:"last_granted_head,omitempty"`
}

var cognitiveTitle = regexp.MustCompile(`^Refactor this method to reduce its Cognitive Complexity from ([1-9][0-9]{0,5}) to the ([1-9][0-9]{0,5}) allowed\.$`)
var ciRuleID = regexp.MustCompile(`^(?:csharpsquid:)?S[0-9]{3,6}$`)

func ciSourcePath(path string) bool {
	return len(path) <= 240 && utf8.ValidString(path) && sourceRelative(path) && !excluded(path) && !strings.Contains(strings.ToLower("/"+path+"/"), "/.sdlc/") && strings.IndexFunc(path, unicode.IsControl) < 0
}

func validateCIResult(result CIResult, published Publication) error {
	if err := validateCIFailureContext(result, published); err != nil {
		return err
	}
	if len(result.Diagnostics) > maxCIDiagnostics {
		return fmt.Errorf("CI diagnostic bound exceeded")
	}
	for _, a := range result.Diagnostics {
		if result.Status != "failed" || !objectID.MatchString(a.Head) || a.Head != published.HeadSHA || !ciSourcePath(a.Path) || a.Line < 1 || a.Line > 9999999 || a.EndLine < 0 || a.EndLine > 9999999 || a.EndLine != 0 && a.EndLine < a.Line || a.Level != "failure" && a.Level != "warning" && a.Level != "notice" {
			return fmt.Errorf("invalid CI source diagnostic")
		}
		switch a.Kind {
		case "cognitive_complexity":
			if a.Rule != "" || a.Current <= a.Limit || a.Limit < 1 || a.Current > 999999 {
				return fmt.Errorf("invalid CI complexity diagnostic")
			}
		case "generic":
			if a.Current != 0 || a.Limit != 0 || a.Rule != "" && !ciRuleID.MatchString(a.Rule) {
				return fmt.Errorf("invalid CI rule diagnostic")
			}
		default:
			return fmt.Errorf("unknown CI diagnostic kind")
		}
	}
	return nil
}

func validateCIJournal(j *Journal) error {
	if err := validateCIResult(j.CI, j.Publication); err != nil {
		return err
	}
	if b := j.CIRepairBudget; b != nil {
		if b.Used < 0 || !runLimitsOverridden(j) && b.Used > 12 || b.StartRound < 0 || b.StartRound > j.Rounds || j.Rounds-b.StartRound < b.Used || b.LastGrantedHead != "" && !objectID.MatchString(b.LastGrantedHead) || b.Used == 0 && b.LastGrantedHead != "" || b.Used > 0 && b.LastGrantedHead == "" {
			return fmt.Errorf("invalid CI repair accounting")
		}
	}
	return nil
}

func ciRepairFeedback(result CIResult, head string) string {
	text := "Required CI failed on published revision " + head + ". Inspect the failed CI checks and repair within ticket scope, then commit and request full local checks again."
	for _, a := range result.Diagnostics {
		text += fmt.Sprintf("\nCI %s in committed source %s:%d.", a.Level, a.Path, a.Line)
		if a.Kind == "cognitive_complexity" {
			text += fmt.Sprintf(" Reduce this method's cognitive complexity from %d to at most %d while preserving behavior.", a.Current, a.Limit)
		} else {
			if a.Rule != "" {
				text += " Analyzer rule " + a.Rule + "."
			}
			text += " Inspect this source location and repair the analyzer finding while preserving behavior."
		}
	}
	return text + ciFailureFeedback(result)
}

var actionsJobLogEndpoint = regexp.MustCompile(`^repos/([^/]+/[^/]+)/actions/jobs/([1-9][0-9]*)/logs$`)
var actionsArtifactEndpoint = regexp.MustCompile(`^repos/([^/]+/[^/]+)/actions/artifacts/([1-9][0-9]*)/zip$`)

// Only the fixed controller-generated official-client job-log request can use
// the larger raw intake. All metadata and other argv retain their old bound.
func ciCommandOutputLimit(name string, args []string) int {
	if name != "gh" || len(args) != 5 || args[0] != "api" || args[1] != "--hostname" || args[2] != "github.com" || args[3] != "--allow-escape-sequences" {
		return maxCIAPIBytes
	}
	match := actionsJobLogEndpoint.FindStringSubmatch(args[4])
	limit := maxActionsJobLogBytes
	if len(match) == 0 {
		match = actionsArtifactEndpoint.FindStringSubmatch(args[4])
		limit = maxCIArtifactZIPBytes
	}
	if len(match) != 3 || !githubRepository.MatchString(match[1]) {
		return maxCIAPIBytes
	}
	job, err := strconv.ParseInt(match[2], 10, 64)
	if err != nil || job <= 0 {
		return maxCIAPIBytes
	}
	return limit
}

type ciResponseSizeError struct{ Limit int }

func (e *ciResponseSizeError) Error() string {
	return fmt.Sprintf("GitHub response exceeds %d-byte intake bound", e.Limit)
}

type ciAPIOutput struct {
	limit    int
	buffer   bytes.Buffer
	exceeded bool
}

func (b *ciAPIOutput) Write(p []byte) (int, error) {
	limit := b.limit
	if limit == 0 {
		limit = maxCIAPIBytes
	}
	if b.exceeded || len(p) > limit-b.buffer.Len() {
		b.exceeded = true
		return len(p), nil
	}
	return b.buffer.Write(p)
}
func (p GitHubPublisher) ciAPI(ctx context.Context, endpoint string, target any) error {
	data, err := p.command(ctx, "gh", "api", "--hostname", "github.com", endpoint)
	if err != nil || len(data) > maxCIAPIBytes || !utf8.Valid(data) {
		return fmt.Errorf("CI annotation API could not be read")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(target) != nil || decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("invalid CI annotation API response")
	}
	return nil
}

type ciCheckRun struct {
	DetailsURL string `json:"details_url"`
	HTMLURL    string `json:"html_url"`
	App        struct {
		ID   int64  `json:"id"`
		Slug string `json:"slug"`
	} `json:"app"`
	ID         int64  `json:"id"`
	Head       string `json:"head_sha"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Output     struct {
		Count   int    `json:"annotations_count"`
		Title   string `json:"title"`
		Summary string `json:"summary"`
		Text    string `json:"text"`
	} `json:"output"`
}

func validCICheckStatus(s string) bool {
	switch s {
	case "queued", "in_progress", "completed", "waiting", "requested", "pending":
		return true
	}
	return false
}

func failedCIConclusion(s string) bool {
	return s == "failure" || s == "timed_out" || s == "cancelled" || s == "action_required" || s == "stale"
}

func nativeSonarAnalysisName(name string) bool {
	return name == "SonarCloud Code Analysis" || name == "SonarCloudAnalysis"
}

func (p GitHubPublisher) collectCIAnnotations(ctx context.Context, plan Plan, pr Publication, sonarNames map[string]bool) ([]CIAnnotation, error) {
	root := "repos/" + plan.Repository
	var runs []ciCheckRun
	var workflows []ciCheckRun
	matched := map[string]bool{}
	total := -1
	seen := map[int64]bool{}
	for page := 1; page <= 2; page++ {
		var response struct {
			Total int          `json:"total_count"`
			Runs  []ciCheckRun `json:"check_runs"`
		}
		if err := p.ciAPI(ctx, fmt.Sprintf("%s/commits/%s/check-runs?per_page=100&page=%d", root, pr.HeadSHA, page), &response); err != nil {
			return nil, err
		}
		if response.Runs == nil || response.Total < 0 || response.Total > 200 || len(response.Runs) > 100 || total >= 0 && total != response.Total {
			return nil, fmt.Errorf("invalid CI check-run listing")
		}
		total = response.Total
		for _, run := range response.Runs {
			if run.ID < 1 || seen[run.ID] || run.Head != pr.HeadSHA || run.Name == "" || len(run.Name) > 240 || !validCICheckStatus(run.Status) {
				return nil, fmt.Errorf("CI check-run identity changed")
			}
			seen[run.ID] = true
			if sonarNames[run.Name] && run.Status == "completed" && failedCIConclusion(run.Conclusion) {
				if matched[run.Name] {
					return nil, fmt.Errorf("failed Sonar candidate has ambiguous check-run identity")
				}
				matched[run.Name] = true
				if run.App.ID == 15368 && run.App.Slug == "github-actions" && !nativeSonarAnalysisName(run.Name) {
					workflows = append(workflows, run)
					continue
				}
				if run.App.Slug != "sonarqubecloud" && run.App.Slug != "sonarcloud" && run.App.Slug != "sonarqube" {
					return nil, fmt.Errorf("failed Sonar check has an unexpected app identity")
				}
				runs = append(runs, run)
			}
		}
		if len(seen) == total {
			break
		}
		if len(response.Runs) != 100 || page == 2 {
			return nil, fmt.Errorf("incomplete CI check-run listing")
		}
	}
	if len(runs)+len(workflows) == 0 || len(runs)+len(workflows) > 4 || len(matched) != len(sonarNames) {
		return nil, fmt.Errorf("failed Sonar check-run could not be identified")
	}
	for _, listed := range workflows {
		var run ciCheckRun
		if err := p.ciAPI(ctx, fmt.Sprintf("%s/check-runs/%d", root, listed.ID), &run); err != nil {
			return nil, err
		}
		if run.ID != listed.ID || run.Head != pr.HeadSHA || run.Name != listed.Name || run.App.ID != 15368 || run.App.Slug != "github-actions" || run.Status != "completed" || run.Conclusion != listed.Conclusion || !failedCIConclusion(run.Conclusion) {
			return nil, fmt.Errorf("workflow check-run changed during CI inspection")
		}
	}
	// Verified Actions jobs remain ordinary failed CI. Their name does not
	// authorize native Sonar annotations, source details or log forwarding.
	if len(runs) == 0 {
		return nil, nil
	}
	var commit struct {
		SHA  string `json:"sha"`
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if err := p.ciAPI(ctx, root+"/git/commits/"+pr.HeadSHA, &commit); err != nil {
		return nil, err
	}
	if commit.SHA != pr.HeadSHA || !objectID.MatchString(commit.Tree.SHA) {
		return nil, fmt.Errorf("CI source commit changed")
	}
	var tree struct {
		SHA       string `json:"sha"`
		Truncated bool   `json:"truncated"`
		Entries   []struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
			Type string `json:"type"`
		} `json:"tree"`
	}
	if err := p.ciAPI(ctx, root+"/git/trees/"+commit.Tree.SHA+"?recursive=1", &tree); err != nil {
		return nil, err
	}
	if tree.SHA != commit.Tree.SHA || tree.Truncated || tree.Entries == nil || len(tree.Entries) > 10000 {
		return nil, fmt.Errorf("CI source tree unavailable or incomplete")
	}
	approved := map[string]bool{}
	for _, entry := range tree.Entries {
		if (entry.Mode == "100644" || entry.Mode == "100755") && entry.Type == "blob" && ciSourcePath(entry.Path) {
			approved[entry.Path] = true
		}
	}
	var records []CIAnnotation
	for _, listed := range runs {
		var run ciCheckRun
		if err := p.ciAPI(ctx, fmt.Sprintf("%s/check-runs/%d", root, listed.ID), &run); err != nil {
			return nil, err
		}
		if run.ID != listed.ID || run.Head != pr.HeadSHA || run.Name != listed.Name || run.App.ID != listed.App.ID || run.App.Slug != listed.App.Slug || run.Status != "completed" || !failedCIConclusion(run.Conclusion) || run.Output.Count < 0 || run.Output.Count > 200 {
			return nil, fmt.Errorf("CI check-run changed during annotation collection")
		}
		count := 0
		for page := 1; count < run.Output.Count; page++ {
			var annotations []struct {
				Path  string `json:"path"`
				Start int    `json:"start_line"`
				End   int    `json:"end_line"`
				Level string `json:"annotation_level"`
				Title string `json:"title"`
			}
			if err := p.ciAPI(ctx, fmt.Sprintf("%s/check-runs/%d/annotations?per_page=100&page=%d", root, run.ID, page), &annotations); err != nil {
				return nil, err
			}
			if annotations == nil || len(annotations) > 100 || len(annotations) == 0 || count+len(annotations) > run.Output.Count || page > 2 {
				return nil, fmt.Errorf("invalid CI annotation page")
			}
			count += len(annotations)
			for _, a := range annotations {
				if a.Start < 1 || a.Start > 9999999 || a.End < a.Start || a.End > 9999999 || a.Level != "failure" && a.Level != "warning" && a.Level != "notice" {
					return nil, fmt.Errorf("invalid CI annotation location")
				}
				if !approved[a.Path] {
					continue
				}
				record := CIAnnotation{Head: pr.HeadSHA, Path: a.Path, Line: a.Start, EndLine: a.End, Level: a.Level, Kind: "generic"}
				if match := cognitiveTitle.FindStringSubmatch(a.Title); match != nil {
					current, _ := strconv.Atoi(match[1])
					limit, _ := strconv.Atoi(match[2])
					if current > limit {
						record.Kind = "cognitive_complexity"
						record.Current = current
						record.Limit = limit
					}
				} else if ciRuleID.MatchString(a.Title) {
					record.Rule = a.Title
				}
				records = append(records, record)
			}
			if count < run.Output.Count && len(annotations) != 100 {
				return nil, fmt.Errorf("incomplete CI annotation page")
			}
		}
		var after ciCheckRun
		if err := p.ciAPI(ctx, fmt.Sprintf("%s/check-runs/%d", root, run.ID), &after); err != nil {
			return nil, err
		}
		if after.ID != run.ID || after.Head != run.Head || after.Name != run.Name || after.App.ID != run.App.ID || after.App.Slug != run.App.Slug || after.Status != run.Status || after.Conclusion != run.Conclusion || after.Output.Count != run.Output.Count {
			return nil, fmt.Errorf("CI check-run changed after annotation collection")
		}
	}
	rank := func(level string) int {
		if level == "failure" {
			return 0
		}
		if level == "warning" {
			return 1
		}
		return 2
	}
	sort.SliceStable(records, func(i, j int) bool { return rank(records[i].Level) < rank(records[j].Level) })
	if len(records) > maxCIDiagnostics {
		records = records[:maxCIDiagnostics]
	}
	return records, nil
}

const ciRepairBound = "required CI still fails after the repair bound"
const legacyCIRepairBound = "legacy published run has unknown CI repair accounting; explicitly initialize the CI repair budget for this CI hold"

// ValidateCIRepairInitialization permits only a one-time legacy CI-bound recovery.
func ValidateCIRepairInitialization(j Journal) error {
	if j.CIRepairBudget != nil || j.State != "blocked" || j.ResumeState != "ci" || j.SessionID == "" || j.Publication.Number < 1 || !objectID.MatchString(j.Publication.HeadSHA) || !objectID.MatchString(j.Publication.BaseSHA) || j.CI.Status != "failed" || j.Publication.URL != fmt.Sprintf("https://github.com/%s/pull/%d", j.Plan.Repository, j.Publication.Number) || (j.StopReason != ciRepairBound && j.StopReason != legacyCIRepairBound) {
		return fmt.Errorf("CI repair initialization requires an uninitialized legacy CI-bound hold with its original session and publication")
	}
	return nil
}
