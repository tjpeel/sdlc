package workrun

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type reviewChangedPathRepository interface {
	ReviewChangedPaths(context.Context, string, string, string) ([]string, error)
}

func (r DockerRepository) ReviewChangedPaths(ctx context.Context, workspace, base, head string) ([]string, error) {
	if !objectID.MatchString(base) || !objectID.MatchString(head) {
		return nil, fmt.Errorf("review file inventory requires exact revisions")
	}
	raw, err := r.run(ctx, workspace, "review_files:"+base+":"+head, "")
	if err != nil {
		return nil, err
	}
	var paths []string
	if json.Unmarshal(raw, &paths) != nil || paths == nil {
		return nil, fmt.Errorf("invalid changed-file review inventory")
	}
	seen := map[string]bool{}
	for _, p := range paths {
		if !safeRelative(p) || seen[p] {
			return nil, fmt.Errorf("invalid changed-file review path")
		}
		seen[p] = true
	}
	return paths, nil
}

// Old saved reports remain readable. Current native schemas require inspection
// entries, and a declared incomplete or missing file cannot silently become full
// coverage. This checks the report's accounting, not whether a model truly read.
func (r Runner) reconcileReviewInspection(ctx context.Context, workspace string, out *Outcome) error {
	c := out.ReviewCoverage
	if c == nil || c.Inspection == nil || out.Status != "reviewed" || c.Scope != "full" {
		return nil
	}
	if err := validateReviewDetails(*out, "review"); err != nil {
		return err
	}
	seen := map[string]bool{}
	pending := []string{}
	for _, item := range c.Inspection {
		seen[item.Path] = true
		if !item.Complete {
			pending = append(pending, item.Path+": "+item.Evidence)
		}
	}
	manifestAvailable := false
	if repo, ok := r.Repository.(reviewChangedPathRepository); ok {
		manifestAvailable = true
		paths, err := repo.ReviewChangedPaths(ctx, workspace, c.BaseSHA, c.HeadSHA)
		if err != nil {
			return err
		}
		for _, path := range paths {
			if !seen[path] {
				pending = append(pending, path+": no file-inspection entry supplied")
			}
		}
	}
	if len(c.Inspection) == 0 && !manifestAvailable {
		pending = append(pending, "Record changed-file inspection, including test assertions, fixtures and supporting helpers")
	}
	if len(pending) == 0 {
		return nil
	}
	sort.Strings(pending)
	gap := "Finish material file inspection: " + strings.Join(pending, "; ")
	exists := false
	for _, existing := range c.Gaps {
		if existing == gap {
			exists = true
		}
	}
	if !exists {
		c.Gaps = append(c.Gaps, gap)
	}
	c.Scope = "partial"
	out.Status = "review_incomplete"
	return nil
}
