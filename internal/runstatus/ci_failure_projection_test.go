package runstatus

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/workrun"
)

func TestPrivateCIFailureContextIsOmittedFromPublicProjections(t *testing.T) {
	v := reportView()
	v.Journal.CI = workrun.CIResult{Status: "failed", FailureContext: &workrun.CIFailureContext{Head: strings.Repeat("b", 40), Checks: []workrun.CIFailedCheck{{Kind: "check", Name: "offline unit suite", CheckID: 41, AppID: 90, AppSlug: "example-checks", Conclusion: "failure", Text: "excluded-private-ci-failure-context"}}}}
	public := project(*v.Journal, "/example/run")
	if public.CI.FailureContext != nil || public.CI.Status != "failed" {
		t.Fatal("private CI payload copied into public registry projection")
	}
	report, err := reportSnapshot(public)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "excluded-private-ci-failure-context") || strings.Contains(string(data), "failure_context") {
		t.Fatal("private CI payload copied into durable public report")
	}
}
