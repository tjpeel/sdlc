package workrun

import (
	"strings"
	"testing"
)

func externalBoundaryFixture() ExternalPRBoundary {
	return ExternalPRBoundary{Repository: "example/project", RepositoryID: 1, ParentNumber: 41, ParentURL: "https://github.com/example/project/pull/41", ParentBase: "main", ParentBaseSHA: strings.Repeat("a", 40), ParentBranch: "ticket", ParentHeadSHA: strings.Repeat("b", 40), IncomingNumber: 47, IncomingURL: "https://github.com/example/project/pull/47", IncomingHeadRepository: "contributor/project", IncomingHeadRepositoryID: 2, IncomingHeadBranch: "changes", IncomingHeadSHA: strings.Repeat("c", 40)}
}

func TestExternalPRBoundaryRequiresExactOpenPRs(t *testing.T) {
	b := externalBoundaryFixture()
	o := ExternalPRObservation{Boundary: b, ParentState: "OPEN", IncomingState: "OPEN", IncomingBaseRepositoryID: b.RepositoryID, IncomingBaseBranch: b.ParentBranch, IncomingBaseSHA: b.ParentHeadSHA}
	if err := ValidateExternalPRBeforeMerge(b, o); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*ExternalPRObservation){
		"parent moved":                func(o *ExternalPRObservation) { o.Boundary.ParentHeadSHA = strings.Repeat("d", 40) },
		"incoming moved":              func(o *ExternalPRObservation) { o.Boundary.IncomingHeadSHA = strings.Repeat("d", 40) },
		"wrong target":                func(o *ExternalPRObservation) { o.IncomingBaseBranch = "main" },
		"wrong base repository":       func(o *ExternalPRObservation) { o.IncomingBaseRepositoryID++ },
		"different source repository": func(o *ExternalPRObservation) { o.Boundary.IncomingHeadRepositoryID++ },
		"parent closed":               func(o *ExternalPRObservation) { o.ParentState = "CLOSED" },
		"incoming closed":             func(o *ExternalPRObservation) { o.IncomingState = "CLOSED" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			changed := o
			mutate(&changed)
			if ValidateExternalPRBeforeMerge(b, changed) == nil {
				t.Fatal("accepted changed boundary")
			}
		})
	}
}

func TestExternalPRMergeRequiresBothFrozenParents(t *testing.T) {
	b := externalBoundaryFixture()
	merge := strings.Repeat("d", 40)
	o := ExternalPRObservation{Boundary: b, ParentState: "OPEN", IncomingState: "MERGED", IncomingBaseRepositoryID: b.RepositoryID, IncomingBaseBranch: b.ParentBranch, IncomingBaseSHA: merge, MergeSHA: merge, MergeParents: []string{b.ParentHeadSHA, b.IncomingHeadSHA}}
	o.Boundary.ParentHeadSHA = merge
	if err := ValidateExternalPRMerge(b, o); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*ExternalPRObservation){
		"parent moved after merge": func(o *ExternalPRObservation) { o.Boundary.ParentHeadSHA = strings.Repeat("e", 40) },
		"target moved":             func(o *ExternalPRObservation) { o.IncomingBaseSHA = strings.Repeat("e", 40) },
		"wrong first parent":       func(o *ExternalPRObservation) { o.MergeParents = []string{strings.Repeat("e", 40), b.IncomingHeadSHA} },
		"wrong second parent":      func(o *ExternalPRObservation) { o.MergeParents = []string{b.ParentHeadSHA, strings.Repeat("e", 40)} },
		"squash":                   func(o *ExternalPRObservation) { o.MergeParents = []string{b.ParentHeadSHA} },
		"changed target":           func(o *ExternalPRObservation) { o.IncomingBaseBranch = "main" },
		"incoming not merged":      func(o *ExternalPRObservation) { o.IncomingState = "OPEN" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			changed := o
			mutate(&changed)
			if ValidateExternalPRMerge(b, changed) == nil {
				t.Fatal("accepted unproven merge")
			}
		})
	}
}

func TestExternalPRBoundaryRejectsInvalidSavedIdentity(t *testing.T) {
	b := externalBoundaryFixture()
	o := ExternalPRObservation{Boundary: b, ParentState: "OPEN", IncomingState: "OPEN", IncomingBaseRepositoryID: b.RepositoryID, IncomingBaseBranch: b.ParentBranch, IncomingBaseSHA: b.ParentHeadSHA}
	cases := map[string]func(*ExternalPRBoundary){
		"same PR":               func(b *ExternalPRBoundary) { b.IncomingNumber = b.ParentNumber },
		"wrong URL":             func(b *ExternalPRBoundary) { b.IncomingURL = "https://github.com/example/other/pull/47" },
		"invalid SHA":           func(b *ExternalPRBoundary) { b.IncomingHeadSHA = "short" },
		"invalid repository ID": func(b *ExternalPRBoundary) { b.RepositoryID = 0 },
		"same branch": func(b *ExternalPRBoundary) {
			b.IncomingHeadRepositoryID = b.RepositoryID
			b.IncomingHeadBranch = b.ParentBranch
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			changed := b
			mutate(&changed)
			observed := o
			observed.Boundary = changed
			if ValidateExternalPRBeforeMerge(changed, observed) == nil {
				t.Fatal("accepted invalid identity")
			}
		})
	}
}
