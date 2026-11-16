package workrun

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestApprovedAmendmentIsBoundToLocalFeedbackFingerprint(t *testing.T) {
	text := "Approved specification amendment A1\nTarget captured requirement: specification.md\n\nChange the requirement"
	c := PRFeedbackComment{ID: localFeedbackID(text), Kind: "local", Author: "local", Body: text, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Amendment: true}
	if err := validateFeedbackComment(c); err != nil {
		t.Fatal(err)
	}
	fp := feedbackFingerprint(c)
	c.Amendment = false
	if feedbackFingerprint(c) == fp {
		t.Fatal("amendment authority is not bound to the feedback fingerprint")
	}
	c.Amendment = true
	c.Kind = "comment"
	if err := validateFeedbackComment(c); err == nil {
		t.Fatal("remote comment was accepted as an approved amendment")
	}
}

func TestAmendmentFlagCannotBeInferredFromBody(t *testing.T) {
	text := "Approved specification amendment A1\nTarget captured requirement: specification.md\n\nChange"
	c := PRFeedbackComment{ID: localFeedbackID(text), Kind: "local", Author: "local", Body: text, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if err := validateFeedbackComment(c); err != nil {
		t.Fatal(err)
	}
	if c.Amendment || !strings.Contains(c.Body, "Approved specification amendment") {
		t.Fatal("body text assigned amendment authority")
	}
}

func TestApprovedAmendmentRetainsAuthorityThroughFeedbackCycle(t *testing.T) {
	dir, j, r, pub, _ := feedbackReadyRun(t)
	provider := &localAssessmentProvider{j: j}
	r.Publisher, r.Provider, r.ProcessPRFeedback = &localFeedbackPublisher{feedbackPublisherFake: pub}, provider, true
	r.LocalPRFeedback = "Approved specification amendment A1\nTarget captured requirement: specification.md\n\nChange the requirement"
	r.LocalAmendment = true
	if err := r.Run(context.Background(), dir, j, ""); err != nil {
		t.Fatal(err)
	}
	if len(j.PRFeedback.Entries) != 1 || !j.PRFeedback.Entries[0].Comment.Amendment {
		t.Fatal("amendment authority lost from journal")
	}
	if len(provider.sessions) != 1 || !strings.Contains(provider.sessions[0].Prompt, "explicitly approved requirements for this run") {
		t.Fatal("implementation did not receive amendment authority")
	}
	if j.PRFeedback.Active || j.PRFeedback.Entries[0].Reply == nil {
		t.Fatal("local amendment did not complete privately")
	}
}

func TestApprovedAmendmentPersistsBeyondItsFeedbackCycle(t *testing.T) {
	text := "Approved specification amendment A1\nTarget captured requirement: specification.md\n\nRevised requirement"
	comment := PRFeedbackComment{ID: localFeedbackID(text), Kind: "local", Author: "local", Body: text, Amendment: true}
	j := Journal{PRFeedback: &PRFeedbackHistory{Active: false, Entries: []PRFeedbackEntry{{Comment: comment}, {Comment: comment}, {Comment: PRFeedbackComment{Kind: "local", Body: "ordinary feedback"}}}}}
	got := approvedAmendmentContext(j)
	if strings.Count(got, "Revised requirement") != 1 || strings.Contains(got, "ordinary feedback") {
		t.Fatalf("unexpected amendment context %q", got)
	}
}

func TestComparisonAmendmentMustMatchCapturedInput(t *testing.T) {
	path := ".sdlc/work/REF/amendments/A1.md"
	input := Input{Path: path, SHA256: strings.Repeat("a", 64)}
	p := Plan{Reference: "REF", ComparisonOf: strings.Repeat("a", 24), Inputs: []Input{input}, Amendments: []Input{input}}
	if err := validateComparisonAndAmendments(p); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(approvedAmendmentContext(Journal{Plan: p}), path) {
		t.Fatal("selected amendment absent from provider context")
	}
	p.Amendments[0].SHA256 = strings.Repeat("b", 64)
	if err := validateComparisonAndAmendments(p); err == nil {
		t.Fatal("uncaptured amendment digest accepted")
	}
}
