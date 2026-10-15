package workrun

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/tjpeel/sdlc/internal/runusage"
)

// The inspector returns paths, counts and fixed risk signals, without diff text.
type ReviewDiffFile struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
	Binary  bool   `json:"binary"`
}
type ReviewDiff struct {
	Files     []ReviewDiffFile `json:"files"`
	Sensitive bool             `json:"sensitive"`
	Truncated bool             `json:"truncated"`
}
type reviewDiffReader interface {
	ReviewDiff(context.Context, string, string, string) (ReviewDiff, error)
}
type ReviewEffortDecision struct {
	Base      string                         `json:"base"`
	Head      string                         `json:"head"`
	Model     string                         `json:"model"`
	Selection runusage.ReviewEffortSelection `json:"selection"`
}

func assessReviewEffort(diff ReviewDiff) runusage.ReviewEffortSelection {
	s := runusage.ReviewEffortSelection{Policy: "pr-v1", Effort: "medium", StatisticsAvailable: true, Files: len(diff.Files)}
	trivial := len(diff.Files) > 0
	sensitive := diff.Sensitive
	architecture := false
	substantiveFiles, substantiveLines := 0, 0
	for _, f := range diff.Files {
		s.ChangedLines += f.Added + f.Deleted
		p := strings.ToLower(f.Path)
		ext := path.Ext(p)
		doc := ext == ".md" || ext == ".rst" || ext == ".txt" && (strings.HasPrefix(p, "docs/") || strings.Contains(p, "/docs/") || path.Base(p) == "readme.txt" || path.Base(p) == "license.txt" || path.Base(p) == "changelog.txt")
		// Agent instructions and executable configuration are not documentation.
		if strings.Contains(p, "/.claude/") || strings.HasPrefix(p, ".claude/") || strings.HasPrefix(p, ".codex/") || strings.Contains(p, "/.codex/") || strings.HasPrefix(p, ".agents/") || strings.HasPrefix(p, ".cursor/") || p == ".github/copilot-instructions.md" || path.Base(p) == "agents.md" || path.Base(p) == "claude.md" || path.Base(p) == "skill.md" {
			doc = false
		}
		trivial = trivial && doc && !f.Binary
		generated := generatedReviewPath(p)
		if !doc && !generated {
			substantiveFiles++
			substantiveLines += f.Added + f.Deleted
		}
		architecture = architecture || ext == ".proto" || ext == ".graphql" || ext == ".sql" || strings.Contains(p, "/contracts/") || strings.HasPrefix(p, "contracts/") || strings.Contains(p, "openapi")
		if !doc && !generated {
			for _, marker := range []string{"auth", "permission", "credential", "secret", "migration", "concurren", "locking", "crypt", "security", "terraform", ".github/workflows/"} {
				sensitive = sensitive || strings.Contains(p, marker)
			}
		}
	}
	switch {
	case diff.Truncated:
		s.Effort, s.Reasons = "high", []string{"diff exceeds bounded assessment capacity"}
	case sensitive:
		s.Effort, s.Reasons = "high", []string{"security, data integrity or concurrency signals in changed source"}
	case architecture:
		s.Effort, s.Reasons = "high", []string{"public contract, API or persistence schema changes"}
	case substantiveFiles >= 25 || substantiveLines >= 1000:
		s.Effort, s.Reasons = "high", []string{"large substantive change: at least 25 files or 1000 changed lines"}
	case trivial && s.Files <= 10 && s.ChangedLines <= 200:
		s.Effort, s.Reasons = "low", []string{"small documentation-only change"}
	default:
		s.Reasons = []string{"ordinary or uncertain change; medium is the baseline"}
	}
	return s
}

func selectedReviewModel(j *Journal) Model {
	m := j.Plan.Roles.Review
	if d := j.ReviewEffort; j.Plan.Roles.ReviewEffortPolicy == "pr-v1" && d != nil && d.Base == j.Publication.BaseSHA && d.Head == j.Publication.HeadSHA && d.Model == m.Name {
		m.Effort = d.Selection.Effort
	}
	return m
}

func (r Runner) selectReviewEffort(ctx context.Context, directory string, j *Journal, workspace string) error {
	if j.Plan.Roles.ReviewEffortPolicy != "pr-v1" || reviewAttemptBoundary(j) == nil {
		return nil
	}
	if d := j.ReviewEffort; d == nil || d.Base != j.Publication.BaseSHA || d.Head != j.Publication.HeadSHA || d.Model != j.Plan.Roles.Review.Name {
		s := runusage.ReviewEffortSelection{Policy: "pr-v1", Effort: "medium", Reasons: []string{"diff assessment unavailable; medium is the baseline"}}
		if reader, ok := r.Repository.(reviewDiffReader); ok {
			if diff, err := reader.ReviewDiff(ctx, workspace, j.Publication.BaseSHA, j.Publication.HeadSHA); err == nil {
				s = assessReviewEffort(diff)
			}
		}
		j.ReviewEffort = &ReviewEffortDecision{Base: j.Publication.BaseSHA, Head: j.Publication.HeadSHA, Model: j.Plan.Roles.Review.Name, Selection: s}
		if err := r.save(directory, j); err != nil {
			return err
		}
	}
	s := j.ReviewEffort.Selection
	fmt.Fprintf(r.Output, "Review effort: %s (automatic; %s).\n", s.Effort, strings.Join(s.Reasons, "; "))
	return nil
}

func validateReviewEffort(j *Journal) error {
	d := j.ReviewEffort
	if d == nil {
		return nil
	}
	s := d.Selection
	if err := s.Validate(); err != nil {
		return err
	}
	if j.Plan.Roles.ReviewEffortPolicy != "pr-v1" || j.Plan.Roles.Review.Provider != "claude" || d.Model != j.Plan.Roles.Review.Name || !objectID.MatchString(d.Base) || !objectID.MatchString(d.Head) {
		return fmt.Errorf("invalid review effort selection")
	}
	return nil
}

// ReviewEffortLabel describes a planned selection before the final PR exists.
func ReviewEffortLabel(roles Roles) string {
	if roles.ReviewEffortPolicy == "pr-v1" {
		return "automatic from PR; medium baseline"
	}
	return roles.Review.Effort
}

func generatedReviewPath(p string) bool {
	switch path.Base(p) {
	case "package-lock.json", "npm-shrinkwrap.json", "pnpm-lock.yaml", "yarn.lock", "go.sum", "cargo.lock", "gemfile.lock", "composer.lock", "packages.lock.json", "poetry.lock", "uv.lock":
		return true
	}
	return strings.HasSuffix(p, ".g.cs") || strings.Contains(p, ".generated.")
}
