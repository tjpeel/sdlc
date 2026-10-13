package workrun

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestDerivedPublicationBoundaryRejectsStaleParentAndIntegration(t *testing.T) {
	base := strings.Repeat("1", 40)
	head := strings.Repeat("2", 40)
	merge := strings.Repeat("3", 40)
	for _, test := range []struct {
		name, parentState, observedState, observedHead, observedMerge, integration, branch string
		fails                                                                              bool
	}{
		{"ready", "ready", "OPEN", head, "", base, head, false},
		{"squash merged", "merged", "MERGED", head, merge, base, "", false},
		{"head edit", "ready", "OPEN", merge, "", base, head, true},
		{"branch edit", "ready", "OPEN", head, "", base, merge, true},
		{"merge changed", "merged", "MERGED", head, head, base, "", true},
		{"state changed", "ready", "MERGED", head, merge, base, head, true},
		{"integration advanced", "ready", "OPEN", head, "", merge, head, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := Plan{Repository: "example/project", Base: "main", BaseSHA: base, Derived: &DerivedBoundary{Parents: []DerivedParent{{Branch: "a", Base: "main", BaseSHA: base, HeadSHA: head, State: test.parentState, MergeSHA: merge, Number: 1}, {Branch: "b", Base: "main", BaseSHA: base, HeadSHA: head, State: test.parentState, MergeSHA: merge, Number: 2}}}}
			pub := GitHubPublisher{Frozen: frozenTestIdentity(), Command: func(_ context.Context, name string, args ...string) ([]byte, error) {
				joined := strings.Join(args, " ")
				switch {
				case name == "gh" && joined == "api user":
					return []byte(`{"id":123,"login":"example"}`), nil
				case name == "gh" && strings.HasPrefix(joined, "auth status"):
					return nil, nil
				case name == "git" && strings.HasPrefix(joined, "check-ref-format"):
					return nil, nil
				case name == "gh" && joined == "api repos/example/project":
					return []byte(`{"id":99,"full_name":"example/project"}`), nil
				case name == "gh" && strings.HasPrefix(joined, "api repos/example/project/branches/"):
					value := test.integration
					if !strings.HasSuffix(joined, "/main") {
						value = test.branch
					}
					return []byte(fmt.Sprintf(`{"commit":{"sha":%q}}`, value)), nil
				case name == "gh" && strings.HasPrefix(joined, "pr checks "):
					return []byte(`[{"name":"required","bucket":"pass","state":"SUCCESS"}]`), nil
				case name == "gh" && strings.HasPrefix(joined, "pr view "):
					branch := "a"
					if args[2] == "2" {
						branch = "b"
					}
					return []byte(fmt.Sprintf(`{"state":%q,"headRefOid":%q,"headRefName":%q,"baseRefName":"main","baseRefOid":"`+base+`","mergeCommit":{"oid":%q}}`, test.observedState, test.observedHead, branch, test.observedMerge)), nil
				case name == "git" && strings.Contains(joined, "ls-remote"):
					branch := "a"
					if strings.HasSuffix(joined, "refs/heads/b") {
						branch = "b"
					}
					return []byte(test.branch + "\trefs/heads/" + branch + "\n"), nil
				default:
					t.Fatalf("unexpected command: %s %v", name, args)
					return nil, nil
				}
			}}
			err := pub.verifyDerivedBoundary(context.Background(), plan)
			if (err != nil) != test.fails {
				t.Fatalf("boundary result %v, failure expected %v", err, test.fails)
			}
		})
	}
}
func TestDerivedPublicationRefusesMissingSourceCheckEvidenceBeforeCredentials(t *testing.T) {
	calls := 0
	publisher := DockerPublisher{ValidatePair: func() error { calls++; return nil }}
	if _, err := publisher.PublishDerived(context.Background(), Plan{Derived: &DerivedBoundary{}}, t.TempDir(), Revision{}, CheckEvidence{}); err == nil || calls != 0 {
		t.Fatalf("unchecked derived publication reached credential boundary: %v, %d", err, calls)
	}
}
