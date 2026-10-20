package workrun

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// This is an implementation-only receipt. It never satisfies independent review.
type SelfReviewBoundary struct {
	Head       string `json:"head"`
	Tree       string `json:"tree"`
	ContextKey string `json:"context_key"`
	SessionID  string `json:"session_id"`
}

func captureSelfReview(j *Journal, current Revision) {
	if j.Outcome.Status != "implemented" || !j.Outcome.LocalReview || len(j.Outcome.Findings) > 0 || len(j.Outcome.Questions) > 0 || !j.Evidence.Passed || !sessionIdentifier.MatchString(j.SessionID) {
		return
	}
	if !current.Clean || current.Head != j.Evidence.Head || current.Tree != j.Evidence.Tree || !verificationCurrent(j, current.Head, current.Tree) {
		return
	}
	j.SelfReview = &SelfReviewBoundary{Head: current.Head, Tree: current.Tree, ContextKey: incrementalContextKey(j), SessionID: j.SessionID}
}

func (r Runner) implementationSelfReviewPrompt(ctx context.Context, j *Journal, workspace, resume string) string {
	baseline := j.SelfReview
	if baseline == nil || baseline.SessionID != resume || baseline.ContextKey != incrementalContextKey(j) {
		return ""
	}
	trees, ok := r.Repository.(reviewTreeReader)
	if !ok {
		return ""
	}
	diffs, ok := r.Repository.(reviewDiffReader)
	if !ok {
		return ""
	}
	oldTree, err := trees.ReviewTree(ctx, workspace, baseline.Head)
	if err != nil || oldTree != baseline.Tree {
		return ""
	}
	current, err := r.Repository.Inspect(ctx, workspace)
	if err != nil || !current.Clean {
		return ""
	}
	mode := "unchanged"
	paths := []string{}
	if current.Tree != baseline.Tree {
		diff, err := diffs.ReviewDiff(ctx, workspace, baseline.Head, current.Head)
		if err != nil || !boundedCorrection(diff) {
			return ""
		}
		mode = "correction"
		for _, f := range diff.Files {
			paths = append(paths, f.Path)
		}
		sort.Strings(paths)
	}
	data, _ := json.Marshal(struct {
		Mode         string              `json:"mode"`
		Baseline     *SelfReviewBoundary `json:"baseline"`
		Head, Tree   string
		ChangedPaths []string `json:"changed_paths"`
	}{mode, baseline, current.Head, current.Tree, paths})
	return "\nVerified implementation self-review baseline in this original session: " + string(data) + ". Retain your earlier complete-diff assessment. For unchanged content, assess new feedback and verification evidence; do not reread the entire PR or repeat a completed self-review. For corrections, inspect all changes since the baseline and affected callers/tests/docs/contracts, resolve the current feedback and check the combined final diff for scope/interaction regressions. Reopen any affected earlier conclusion. If impact cannot be bounded or requirements change, perform a full self-review. Complete focused self-review before requesting the readiness checks; after checks, reassess only new edits or evidence. Preserve current-head required checks and local_review truthfulness. This receipt does not satisfy or replace independent review. Record the reviewed head/tree and concise findings in summary; do not launch another whole-PR reviewer.\n"
}

func validateSelfReview(j *Journal) error {
	p := j.SelfReview
	if p != nil && (!objectID.MatchString(p.Head) || !objectID.MatchString(p.Tree) || len(p.ContextKey) != 64 || !sessionIdentifier.MatchString(p.SessionID)) {
		return fmt.Errorf("invalid implementation self-review receipt")
	}
	return nil
}

// Successful stdout remains in the journal and private check log. Keep command
// identities/exits and evidence boundaries in prompts, with all failure context.
func compactSuccessfulOutputs(outputs []CommandOutput) []CommandOutput {
	copy := append([]CommandOutput(nil), outputs...)
	for i := range copy {
		o := &copy[i]
		if o.Completed && o.ExitCode == 0 && o.FailureBytes == 0 {
			o.CombinedOutput = ""
			o.Encoding = ""
			o.HeadBytes = 0
			o.TailBytes = 0
			o.OmittedBytes = o.TotalBytes
			o.Truncated = o.TotalBytes > 0
		}
	}
	return copy
}
