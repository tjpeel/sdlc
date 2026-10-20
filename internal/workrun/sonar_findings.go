package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type SonarIssue struct {
	Key     string `json:"key"`
	Project string `json:"project"`
	Rule    string `json:"rule"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
	Line    int    `json:"line,omitempty"`
}

// Findings are separate from CI status: a passed quality gate may leave issues.
// Presence means the complete current-head PR issue listing was collected.
type SonarFindings struct {
	Head     string                `json:"head"`
	Projects []string              `json:"projects"`
	Issues   []SonarIssue          `json:"issues"`
	Groups   []SonarDuplicateGroup `json:"duplication_groups"`
}

func sonarText(s string, limit int) bool {
	return s != "" && len(s) <= limit && utf8.ValidString(s) && strings.IndexFunc(s, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) < 0
}
func validateSonarFindings(f *SonarFindings, pr Publication) error {
	if f == nil {
		return nil
	}
	if f.Head != pr.HeadSHA || !objectID.MatchString(f.Head) || len(f.Projects) == 0 {
		return fmt.Errorf("invalid Sonar findings revision")
	}
	projects := map[string]bool{}
	for _, p := range f.Projects {
		if !sonarText(p, 240) || projects[p] {
			return fmt.Errorf("invalid Sonar project identity")
		}
		projects[p] = true
	}
	seen := map[string]bool{}
	for _, i := range f.Issues {
		key := i.Project + ":" + i.Key
		if !projects[i.Project] || !sonarText(i.Key, 240) || seen[key] || !sonarText(i.Rule, 240) || !sonarText(i.Message, 4000) || i.Line < 0 || i.Line > 9999999 || i.Path != "" && !ciSourcePath(i.Path) || i.Path == "" && i.Line != 0 {
			return fmt.Errorf("invalid Sonar issue evidence")
		}
		seen[key] = true
	}
	for _, g := range f.Groups {
		if len(g.Ranges) < 2 || len(g.Ranges) > 100 {
			return fmt.Errorf("invalid Sonar duplicate group")
		}
		for _, span := range g.Ranges {
			if !ciSourcePath(span.Path) || span.Line < 1 || span.EndLine < span.Line || span.EndLine > 9999999 {
				return fmt.Errorf("invalid Sonar duplicate range")
			}
		}
	}
	return nil
}

var errSonarAnalysisPending = errors.New("waiting for current-head Sonar analysis")

func (p GitHubPublisher) verifySonarPR(ctx context.Context, project string, pr Publication) error {
	var listing struct {
		PullRequests []struct {
			Key    string
			Commit struct{ SHA string }
		} `json:"pullRequests"`
	}
	if err := p.sonarJSON(ctx, "project_pull_requests/list?project="+url.QueryEscape(project), &listing); err != nil {
		return err
	}
	for _, candidate := range listing.PullRequests {
		if candidate.Key == strconv.Itoa(pr.Number) && candidate.Commit.SHA == pr.HeadSHA {
			return nil
		}
	}
	return fmt.Errorf("%w", errSonarAnalysisPending)
}
func (p GitHubPublisher) sonarSourcePaths(ctx context.Context, plan Plan, pr Publication) (map[string]bool, error) {
	var commit struct {
		SHA  string
		Tree struct{ SHA string }
	}
	if err := p.ciAPI(ctx, "repos/"+plan.Repository+"/git/commits/"+pr.HeadSHA, &commit); err != nil {
		return nil, err
	}
	if commit.SHA != pr.HeadSHA || !objectID.MatchString(commit.Tree.SHA) {
		return nil, fmt.Errorf("invalid Sonar source tree")
	}
	var tree struct {
		SHA       string
		Truncated bool
		Tree      []struct{ Path, Type, Mode string }
	}
	if err := p.ciAPI(ctx, "repos/"+plan.Repository+"/git/trees/"+commit.Tree.SHA+"?recursive=1", &tree); err != nil {
		return nil, err
	}
	if tree.Truncated || tree.SHA != commit.Tree.SHA {
		return nil, fmt.Errorf("incomplete Sonar source tree")
	}
	paths := map[string]bool{}
	for _, e := range tree.Tree {
		if e.Type == "blob" && (e.Mode == "100644" || e.Mode == "100755") && ciSourcePath(e.Path) {
			paths[e.Path] = true
		}
	}
	return paths, nil
}

func (p GitHubPublisher) collectSonarFindings(ctx context.Context, plan Plan, pr Publication, measures []SonarDuplication) (*SonarFindings, error) {
	if len(measures) == 0 {
		return nil, fmt.Errorf("Sonar repair policy requires PR-specific analysis evidence; branch-scoped or missing evidence cannot establish a clean PR")
	}
	f := &SonarFindings{Head: pr.HeadSHA, Issues: []SonarIssue{}, Groups: []SonarDuplicateGroup{}}
	projects := map[string]bool{}
	groups := map[string]bool{}
	for _, m := range measures {
		if !validSonarMeasuresURL(m.MeasuresURL, pr.Number) || m.Head != pr.HeadSHA {
			return nil, fmt.Errorf("invalid Sonar analysis scope")
		}
		u, _ := url.Parse(m.MeasuresURL)
		projects[u.Query().Get("id")] = true
		for _, g := range m.Groups {
			sort.Slice(g.Ranges, func(a, b int) bool {
				x, y := g.Ranges[a], g.Ranges[b]
				if x.Path != y.Path {
					return x.Path < y.Path
				}
				if x.Line != y.Line {
					return x.Line < y.Line
				}
				return x.EndLine < y.EndLine
			})
			key := sonarGroupKey(g)
			if !groups[key] {
				f.Groups = append(f.Groups, g)
				groups[key] = true
			}
		}
	}
	for project := range projects {
		f.Projects = append(f.Projects, project)
	}
	sort.Strings(f.Projects)
	var paths map[string]bool
	for _, project := range f.Projects {
		if err := p.verifySonarPR(ctx, project, pr); err != nil {
			return nil, err
		}
		for _, m := range measures {
			u, _ := url.Parse(m.MeasuresURL)
			if u.Query().Get("id") == project && m.DetailsStatus != "collected" && m.DetailsStatus != "none" {
				return nil, fmt.Errorf("Sonar duplication details unavailable; repair cannot omit reported duplication")
			}
		}
		total := -1
		seen := map[string]bool{}
		for page := 1; ; page++ {
			var response struct {
				Paging struct{ Total *int }
				Total  *int
				Issues []struct {
					Key, Project, Component, Rule, Message, Status, IssueStatus, Resolution, PullRequest string
					Line                                                                                 int
					TextRange                                                                            struct{ StartLine int }
				}
			}
			q := url.Values{"componentKeys": {project}, "pullRequest": {strconv.Itoa(pr.Number)}, "resolved": {"false"}, "ps": {"100"}, "p": {strconv.Itoa(page)}}
			if err := p.sonarJSON(ctx, "issues/search?"+q.Encode(), &response); err != nil {
				return nil, fmt.Errorf("Sonar issue listing unavailable: %w", err)
			}
			reported := response.Paging.Total
			if reported == nil {
				reported = response.Total
			}
			if reported == nil || *reported < 0 || len(response.Issues) > 100 || total >= 0 && *reported != total {
				return nil, fmt.Errorf("incomplete or changing Sonar issue listing")
			}
			total = *reported
			for _, i := range response.Issues {
				if seen[i.Key] || i.Project != project || i.PullRequest != "" && i.PullRequest != strconv.Itoa(pr.Number) {
					return nil, fmt.Errorf("invalid Sonar issue scope")
				}
				seen[i.Key] = true
				if i.Resolution != "" || i.Status == "RESOLVED" || i.Status == "CLOSED" || i.IssueStatus == "FIXED" || i.IssueStatus == "ACCEPTED" || i.IssueStatus == "FALSE_POSITIVE" {
					continue
				}
				if i.Status != "OPEN" && i.Status != "CONFIRMED" && i.Status != "REOPENED" && i.IssueStatus != "OPEN" && i.IssueStatus != "CONFIRMED" {
					return nil, fmt.Errorf("unknown Sonar issue state")
				}
				issue := SonarIssue{Key: i.Key, Project: project, Rule: i.Rule, Message: i.Message, Line: i.Line}
				if issue.Line == 0 {
					issue.Line = i.TextRange.StartLine
				}
				if i.Component != project {
					if !strings.HasPrefix(i.Component, project+":") {
						return nil, fmt.Errorf("Sonar issue component differs from its project")
					}
					issue.Path = strings.TrimPrefix(i.Component, project+":")
					if paths == nil {
						var err error
						paths, err = p.sonarSourcePaths(ctx, plan, pr)
						if err != nil {
							return nil, err
						}
					}
					if !paths[issue.Path] {
						return nil, fmt.Errorf("Sonar issue path is absent from the published source tree")
					}
				}
				f.Issues = append(f.Issues, issue)
			}
			if len(seen) == total {
				break
			}
			if len(seen) > total || len(response.Issues) != 100 {
				return nil, fmt.Errorf("Sonar issue pagination did not return every issue")
			}
		}
		if err := p.verifySonarPR(ctx, project, pr); err != nil {
			return nil, err
		}
	}
	sort.Slice(f.Issues, func(a, b int) bool {
		x, y := f.Issues[a], f.Issues[b]
		if x.Project != y.Project {
			return x.Project < y.Project
		}
		return x.Key < y.Key
	})
	sort.Slice(f.Groups, func(a, b int) bool { return sonarGroupKey(f.Groups[a]) < sonarGroupKey(f.Groups[b]) })
	return f, validateSonarFindings(f, pr)
}

func sonarGroupKey(g SonarDuplicateGroup) string { data, _ := json.Marshal(g); return string(data) }
