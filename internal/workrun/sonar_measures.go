package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

// SonarDuplicateRange identifies committed source without copying source text.
type SonarDuplicateRange struct {
	Path    string `json:"path"`
	Line    int    `json:"line"`
	EndLine int    `json:"end_line"`
}

type SonarDuplicateGroup struct {
	Ranges []SonarDuplicateRange `json:"ranges"`
}

// SonarDuplication is advisory evidence, not a CI failure or repair directive.
// No raw provider text is retained or forwarded to a reviewer.
type SonarDuplication struct {
	Groups        []SonarDuplicateGroup `json:"groups,omitempty"`
	DetailsStatus string                `json:"details_status,omitempty"`
	Head          string                `json:"head"`
	CheckRunID    int64                 `json:"check_run_id"`
	Percent       float64               `json:"new_code_percent"`
	MeasuresURL   string                `json:"measures_url"`
}

var errSonarBranchScope = errors.New("PR-specific duplication evidence unavailable: the Sonar check summary reports branch-scoped measures; branch measures were excluded from PR review evidence")

var sonarDuplicationMeasure = regexp.MustCompile(`\[([0-9]{1,3}(?:\.[0-9]{1,6})?)% Duplication on New Code\]\((https://sonarcloud\.io/component_measures\?[^\s)]+)\)`)

func validSonarMeasuresURL(address string, number int) bool {
	u, err := url.Parse(address)
	if err != nil || u.Scheme != "https" || u.Host != "sonarcloud.io" || u.Path != "/component_measures" || u.User != nil || u.Fragment != "" {
		return false
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q) != 4 || q.Get("id") == "" || q.Get("pullRequest") != strconv.Itoa(number) || q.Get("metric") != "new_duplicated_lines_density" || q.Get("view") != "list" {
		return false
	}
	for _, values := range q {
		if len(values) != 1 {
			return false
		}
	}
	return true
}

// Branch measures cannot establish duplication in the selected PR diff.
// Recognise only the same trusted URL shape used for PR measures.
func branchScopedSonarMeasuresURL(address string, number int) bool {
	u, err := url.Parse(address)
	if err != nil {
		return false
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q["branch"]) != 1 || q.Get("branch") == "" {
		return false
	}
	if _, exists := q["pullRequest"]; exists {
		return false
	}
	q.Del("branch")
	q.Set("pullRequest", strconv.Itoa(number))
	u.RawQuery = q.Encode()
	return validSonarMeasuresURL(u.String(), number)
}

func validateSonarDuplication(measures []SonarDuplication, pr Publication) error {
	if len(measures) > 4 {
		return fmt.Errorf("too many Sonar duplication measures")
	}
	seen := map[int64]bool{}
	for _, m := range measures {
		if !objectID.MatchString(m.Head) || m.Head != pr.HeadSHA || m.CheckRunID < 1 || seen[m.CheckRunID] || math.IsNaN(m.Percent) || math.IsInf(m.Percent, 0) || m.Percent < 0 || m.Percent > 100 || len(m.MeasuresURL) > 2048 || !validSonarMeasuresURL(m.MeasuresURL, pr.Number) {
			return fmt.Errorf("invalid Sonar duplication evidence")
		}
		if (m.DetailsStatus == "collected" && len(m.Groups) == 0) || (m.DetailsStatus != "collected" && len(m.Groups) != 0) || len(m.Groups) > 200 || (m.DetailsStatus != "" && m.DetailsStatus != "collected" && m.DetailsStatus != "unavailable" && m.DetailsStatus != "none") || m.DetailsStatus == "none" && m.Percent != 0 {
			return fmt.Errorf("invalid Sonar duplication details")
		}
		for _, group := range m.Groups {
			if len(group.Ranges) < 2 || len(group.Ranges) > 100 {
				return fmt.Errorf("invalid Sonar duplicate group")
			}
			for _, span := range group.Ranges {
				if !ciSourcePath(span.Path) || span.Line < 1 || span.EndLine < span.Line || span.EndLine > 9999999 {
					return fmt.Errorf("invalid Sonar duplicate range")
				}
			}
		}
		seen[m.CheckRunID] = true
	}
	return nil
}

func (p GitHubPublisher) collectSonarDuplication(ctx context.Context, plan Plan, pr Publication) ([]SonarDuplication, error) {
	runs, err := p.ciCheckRuns(ctx, plan.Repository, pr.HeadSHA)
	if err != nil {
		return nil, err
	}
	var measures []SonarDuplication
	branchScope := false
	for _, listed := range runs {
		if !nativeSonarAnalysisName(listed.Name) || listed.Status != "completed" {
			continue
		}
		if listed.App.ID < 1 || (listed.App.Slug != "sonarqubecloud" && listed.App.Slug != "sonarcloud" && listed.App.Slug != "sonarqube") {
			return nil, fmt.Errorf("Sonar measures have unexpected check-run authority")
		}
		var run ciCheckRun
		if err := p.ciAPI(ctx, fmt.Sprintf("repos/%s/check-runs/%d", plan.Repository, listed.ID), &run); err != nil {
			return nil, err
		}
		if run.ID != listed.ID || run.Head != pr.HeadSHA || run.Name != listed.Name || run.App.ID != listed.App.ID || run.App.Slug != listed.App.Slug || run.Status != "completed" || run.Conclusion != listed.Conclusion {
			return nil, fmt.Errorf("Sonar measures changed during CI inspection")
		}
		matches := sonarDuplicationMeasure.FindAllStringSubmatch(run.Output.Summary, -1)
		if len(matches) > 1 {
			return nil, fmt.Errorf("ambiguous Sonar duplication measure")
		}
		if len(matches) == 0 {
			continue
		}
		percent, err := strconv.ParseFloat(matches[0][1], 64)
		if err != nil {
			return nil, fmt.Errorf("invalid Sonar duplication percentage")
		}
		if branchScopedSonarMeasuresURL(matches[0][2], pr.Number) {
			branchScope = true
			continue
		}
		measures = append(measures, SonarDuplication{Head: pr.HeadSHA, CheckRunID: run.ID, Percent: percent, MeasuresURL: matches[0][2]})
	}
	if err := validateSonarDuplication(measures, pr); err != nil {
		return nil, err
	}
	for i := range measures {
		if measures[i].Percent <= 0 && !plan.Sonar.RepairsEnabled() {
			continue
		}
		groups, err := p.sonarDuplicateDetails(ctx, plan, pr, measures[i].MeasuresURL)
		if err != nil {
			measures[i].DetailsStatus = "unavailable"
			continue
		}
		if len(groups) == 0 {
			if measures[i].Percent > 0 {
				measures[i].DetailsStatus = "unavailable"
			} else {
				measures[i].DetailsStatus = "none"
			}
		} else {
			measures[i].Groups, measures[i].DetailsStatus = groups, "collected"
		}
	}
	if branchScope {
		return measures, fmt.Errorf("%w (PR #%d)", errSonarBranchScope, pr.Number)
	}
	return measures, nil
}

// Fetch only public SonarCloud JSON. Never forward GitHub/provider credentials,
// follow redirects or accept a host supplied by external response content.
func (p GitHubPublisher) sonarJSON(ctx context.Context, endpoint string, target any) error {
	if p.SonarJSON != nil {
		return p.SonarJSON(ctx, endpoint, target)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("Sonar redirect refused") }}
	req, err := http.NewRequestWithContext(ctx, "GET", "https://sonarcloud.io/api/"+endpoint, nil)
	if err != nil {
		return err
	}
	response, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Sonar details unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("Sonar details unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxCIAPIBytes+1))
	if err != nil || len(data) > maxCIAPIBytes {
		return fmt.Errorf("Sonar response unavailable")
	}
	return json.Unmarshal(data, target)
}

func (p GitHubPublisher) sonarDuplicateDetails(ctx context.Context, plan Plan, pr Publication, address string) ([]SonarDuplicateGroup, error) {
	u, _ := url.Parse(address)
	project := u.Query().Get("id")
	number := strconv.Itoa(pr.Number)
	identity := func() error { return p.verifySonarPR(ctx, project, pr) }
	if err := identity(); err != nil {
		return nil, err
	}
	paths, err := p.sonarSourcePaths(ctx, plan, pr)
	if err != nil {
		return nil, err
	}
	positiveFiles := false
	groups := []SonarDuplicateGroup{}
	seen := map[string]bool{}
	observed, total := 0, -1
	for page := 1; ; page++ {
		var files struct {
			Paging     struct{ Total *int }
			Components []struct {
				Key, Path, PullRequest string
				Measures               []struct {
					Metric  string
					Periods []struct{ Value string }
				}
			}
		}
		endpoint := "measures/component_tree?component=" + url.QueryEscape(project) + "&pullRequest=" + number + "&metricKeys=new_duplicated_lines&qualifiers=FIL&ps=100&p=" + strconv.Itoa(page)
		if err := p.sonarJSON(ctx, endpoint, &files); err != nil {
			return nil, err
		}
		if files.Paging.Total == nil || *files.Paging.Total < 0 || len(files.Components) > 100 || total >= 0 && *files.Paging.Total != total {
			return nil, fmt.Errorf("invalid Sonar file listing")
		}
		total = *files.Paging.Total
		observed += len(files.Components)
		for _, file := range files.Components {
			positive := false
			for _, metric := range file.Measures {
				if metric.Metric == "new_duplicated_lines" {
					for _, period := range metric.Periods {
						value, err := strconv.ParseFloat(period.Value, 64)
						if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
							return nil, fmt.Errorf("invalid Sonar duplicated-line count")
						}
						positive = positive || value > 0
					}
				}
			}
			if !positive {
				continue
			}
			positiveFiles = true
			if !paths[file.Path] || file.PullRequest != number || file.Key != project+":"+file.Path {
				return nil, fmt.Errorf("invalid Sonar file identity")
			}
			var detail struct {
				Duplications []struct {
					Blocks []struct {
						From, Size int
						Ref        string `json:"_ref"`
					}
				}
				Files map[string]struct{ Key, Name, Project, PullRequest string }
			}
			if err := p.sonarJSON(ctx, "duplications/show?key="+url.QueryEscape(file.Key)+"&pullRequest="+number, &detail); err != nil {
				return nil, err
			}
			for _, dup := range detail.Duplications {
				group := SonarDuplicateGroup{}
				for _, block := range dup.Blocks {
					f, ok := detail.Files[block.Ref]
					if !ok || f.Project != project || f.PullRequest != number || !paths[f.Name] || f.Key != project+":"+f.Name || block.From < 1 || block.Size < 1 || block.From > 9999999-block.Size+1 {
						return nil, fmt.Errorf("invalid Sonar matching range")
					}
					group.Ranges = append(group.Ranges, SonarDuplicateRange{Path: f.Name, Line: block.From, EndLine: block.From + block.Size - 1})
				}
				if len(group.Ranges) < 2 || len(group.Ranges) > 100 {
					return nil, fmt.Errorf("invalid Sonar group")
				}
				encoded, _ := json.Marshal(group)
				if !seen[string(encoded)] {
					groups = append(groups, group)
					seen[string(encoded)] = true
				}
				if len(groups) > 200 {
					return nil, fmt.Errorf("Sonar details exceed evidence bound")
				}
			}
		}
		if observed == total {
			break
		}
		if observed > total || len(files.Components) != 100 {
			return nil, fmt.Errorf("incomplete Sonar file listing")
		}
	}
	if len(groups) == 0 && (positiveFiles || !plan.Sonar.RepairsEnabled()) {
		return nil, fmt.Errorf("Sonar duplicated ranges unavailable")
	}
	if err := identity(); err != nil {
		return nil, err
	}
	return groups, nil
}
