package workseries

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestChangedProvisionalBaseReconcilesBeforeOldChildCI(t *testing.T) {
	for _, status := range []string{"missing", "unverified", "failed"} {
		t.Run(status, func(t *testing.T) {
			s, child, d := provisionalSeriesFixture(t)
			id := strings.Repeat("2", 24)
			s.Results[child.File] = Result{RunID: id, State: "blocked", Base: "parent-branch", BaseSHA: sha("old-base"), HeadSHA: sha("child-head"), DependencyInvalid: true, StopReason: "old CI hold"}
			d.observe[id] = Observation{State: "OPEN", Base: "parent-branch", BaseSHA: sha("old-base"), HeadSHA: sha("child-head"), CI: status, Details: "current published CI could not be verified"}
			d.reconcileErr = errors.New("specific preflight failure")
			d.reconcileResult = s.Results[child.File]
			r := Runner{Driver: d}
			_, err := r.observeReady(context.Background(), &s, func() error { return nil }, func(string, ...any) {}, true)
			if err != nil {
				t.Fatal(err)
			}
			got := s.Results[child.File]
			if len(d.reconciled) != 1 || !got.DependencyInvalid || !strings.Contains(got.StopReason, "specific preflight failure") || strings.Contains(got.StopReason, "current published CI could not be verified") {
				t.Fatalf("stale CI hid preflight failure or fence: %+v", got)
			}
		})
	}
}
