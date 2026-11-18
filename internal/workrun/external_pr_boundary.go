package workrun

import (
	"fmt"
	"strings"
)

// ExternalPRBoundary freezes both PRs before an incoming PR is merged into
// the branch owned by a run. Repository IDs bind the observations to the
// same GitHub repositories even if names are later reused.
type ExternalPRBoundary struct {
	Repository               string `json:"repository"`
	RepositoryID             int64  `json:"repository_id"`
	ParentNumber             int    `json:"parent_number"`
	ParentURL                string `json:"parent_url"`
	ParentBase               string `json:"parent_base"`
	ParentBaseSHA            string `json:"parent_base_sha"`
	ParentBranch             string `json:"parent_branch"`
	ParentHeadSHA            string `json:"parent_head_sha"`
	IncomingNumber           int    `json:"incoming_number"`
	IncomingURL              string `json:"incoming_url"`
	IncomingHeadRepository   string `json:"incoming_head_repository"`
	IncomingHeadRepositoryID int64  `json:"incoming_head_repository_id"`
	IncomingHeadBranch       string `json:"incoming_head_branch"`
	IncomingHeadSHA          string `json:"incoming_head_sha"`
}

// ExternalPRObservation is a fresh observation of the incoming PR and the
// saved parent PR. A merge result must be independently corroborated with the
// actual commit's parents, not just the response to a merge request.
type ExternalPRObservation struct {
	Boundary                 ExternalPRBoundary
	ParentTree               string
	ParentState              string
	IncomingState            string
	IncomingBaseRepositoryID int64
	IncomingBaseBranch       string
	IncomingBaseSHA          string
	MergeSHA                 string
	MergeParents             []string
}

// validExternalBranch implements the relevant git check-ref-format --branch
// rules without invoking Git while reading an untrusted checkpoint.
func validExternalBranch(branch string) bool {
	if branch == "" || branch == "@" || strings.HasPrefix(branch, "-") || strings.HasPrefix(branch, "/") || strings.HasSuffix(branch, "/") || strings.HasSuffix(branch, ".") || strings.Contains(branch, "..") || strings.Contains(branch, "@{") || strings.Contains(branch, "//") {
		return false
	}
	for _, r := range branch {
		if r <= 32 || r == 127 || strings.ContainsRune("~^:?*[\\", r) {
			return false
		}
	}
	for _, component := range strings.Split(branch, "/") {
		if strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

func (b ExternalPRBoundary) validate() error {
	if !githubRepository.MatchString(b.Repository) || !githubRepository.MatchString(b.IncomingHeadRepository) || b.RepositoryID <= 0 || b.IncomingHeadRepositoryID <= 0 || b.ParentNumber <= 0 || b.IncomingNumber <= 0 || b.ParentNumber == b.IncomingNumber {
		return fmt.Errorf("invalid external PR repository or number")
	}
	if b.ParentURL != fmt.Sprintf("https://github.com/%s/pull/%d", b.Repository, b.ParentNumber) || b.IncomingURL != fmt.Sprintf("https://github.com/%s/pull/%d", b.Repository, b.IncomingNumber) {
		return fmt.Errorf("invalid external PR URL")
	}
	if !objectID.MatchString(b.ParentBaseSHA) || !objectID.MatchString(b.ParentHeadSHA) || !objectID.MatchString(b.IncomingHeadSHA) || !validExternalBranch(b.ParentBase) || !validExternalBranch(b.ParentBranch) || !validExternalBranch(b.IncomingHeadBranch) || b.ParentBase == b.ParentBranch {
		return fmt.Errorf("invalid external PR revision or branch")
	}
	if b.RepositoryID == b.IncomingHeadRepositoryID && b.ParentBranch == b.IncomingHeadBranch {
		return fmt.Errorf("incoming PR head is the parent branch")
	}
	return nil
}

// ValidateExternalPRBeforeMerge rejects any changed identity or revision and
// requires the incoming PR to target the exact saved parent branch and head.
func ValidateExternalPRBeforeMerge(saved ExternalPRBoundary, observed ExternalPRObservation) error {
	if err := saved.validate(); err != nil {
		return err
	}
	if observed.Boundary != saved || observed.ParentState != "OPEN" || observed.IncomingState != "OPEN" || observed.IncomingBaseRepositoryID != saved.RepositoryID || observed.IncomingBaseBranch != saved.ParentBranch || observed.IncomingBaseSHA != saved.ParentHeadSHA || observed.MergeSHA != "" || len(observed.MergeParents) != 0 {
		return fmt.Errorf("external PR or parent boundary changed before merge")
	}
	return nil
}

// ValidateExternalPRMerge proves a merge-commit result is exactly the merge
// of the two frozen heads. It rejects squash and rebase merges because their
// parentage cannot prove this boundary after an interrupted request.
func ValidateExternalPRMerge(saved ExternalPRBoundary, observed ExternalPRObservation) error {
	if err := saved.validate(); err != nil {
		return err
	}
	current := observed.Boundary
	current.ParentHeadSHA = saved.ParentHeadSHA
	if current != saved || observed.ParentState != "OPEN" || observed.IncomingState != "MERGED" || observed.IncomingBaseRepositoryID != saved.RepositoryID || observed.IncomingBaseBranch != saved.ParentBranch || (observed.IncomingBaseSHA != saved.ParentHeadSHA && observed.IncomingBaseSHA != observed.MergeSHA) || !objectID.MatchString(observed.MergeSHA) || observed.Boundary.ParentHeadSHA != observed.MergeSHA || len(observed.MergeParents) != 2 || observed.MergeParents[0] != saved.ParentHeadSHA || observed.MergeParents[1] != saved.IncomingHeadSHA {
		return fmt.Errorf("external PR merge does not prove the saved boundary")
	}
	return nil
}
