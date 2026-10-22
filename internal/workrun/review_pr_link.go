package workrun

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// This private snapshot file makes existing controller evidence available on
// demand. It is neither a tracked repository change nor an extra prompt packet.
const prLinkContextPath = ".git/sdlc-review-context.json"

func writePRLinkReviewContext(workspace string, j Journal) error {
	evidence := j.Evidence
	evidence.Output = nil
	evidence.CompletedFailureFeedback = ""
	additional := append([]VerificationResult(nil), j.Verification...)
	for i := range additional {
		additional[i].Output = nil
	}
	preparation := append([]VerificationResult(nil), j.FormatterPreparation...)
	for i := range preparation {
		preparation[i].Output = nil
	}
	feedback := currentReviewFeedback(feedbackForPrompt(j.PRFeedback))
	if p := j.ReviewPlan; p != nil && p.Publication == j.Publication {
		feedback = correctionReviewFeedback(feedbackForPrompt(j.PRFeedback), p.SourceAttempt)
	}
	packet := map[string]any{
		"publication":                    j.Publication,
		"pr_metadata":                    map[string]string{"title": j.Plan.PRTitle, "body": j.Plan.PRBody},
		"plan":                           selectedReviewRequirements(j.Plan),
		"verification":                   evidence,
		"additional_verification":        additional,
		"formatter_preparation":          preparation,
		"ci":                             reviewCIEvidence{Status: j.CI.Status, SonarDuplication: j.CI.SonarDuplication},
		"human_pr_feedback":              feedback,
		"recorded_review_human_input":    j.ReviewHumanInput,
		"prior_review_assessment":        j.ReviewAssessment,
		"interrupted_independent_review": j.ReviewInterruption,
		"companion_sources":              j.CompanionRepositories,
	}
	raw, err := json.Marshal(packet)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(workspace, prLinkContextPath), raw, 0600)
}

func prLinkReviewPrompt(j Journal) string {
	publication, _ := json.Marshal(j.Publication)
	return "Follow the native global shared instructions. Experimental pr-link review: independently review this PR, choosing your own inspection order and focused tool reads. Pinned publication: " + string(publication) + ".\n" +
		"Use tjpeel-pr-review. /workspace is the exact published head with base history available. Use local Git and source reads to inspect the PR; the URL identifies it, but this isolated worker has no GitHub publication credentials. Do not authenticate, fetch a moving PR head, modify code, run repository checks, post comments, publish or merge. Do not read implementation transcripts.\n" +
		"The existing controller context is available on demand at /workspace/" + prLinkContextPath + ". Select relevant JSON fields rather than loading the entire file. It contains current PR title/body, selected requirements and their input paths, controller verification/CI evidence, human feedback and companion-source paths. Read selected requirements, current claims and applicable feedback before concluding; they constrain scope, not your inspection order. Cite controller checks separately and do not claim you ran them. For missing companion source return checks_requested with repository_requests only; never authenticate yourself. Report actual access or evidence limitations.\n" +
		"Return the supplied review JSON schema, including actionable findings, evidence-backed maintenance/scope recommendations and review_coverage anchored to the exact supplied base/head. Inspect all material changed production code, tests and docs and trace affected producers/consumers in the full checkout. Record exact individual paths and substantive completeness in review_coverage.inspection. Passing checks or a file inventory do not establish inspection. A completed review needs full material coverage or a controller-verified cumulative plan. Accessible unfinished work requires review_incomplete and bounded named gaps; the controller continues this same session and checkout. Use blocked or waiting_for_human only for real access, usage, evidence or decision boundaries. Retain findings through continuation, stop on lack of progress, and wait for any required delegate before final handoff.\n" + incrementalReviewInstructions(&j)
}
