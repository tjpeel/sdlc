package workrun

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type externalPull struct {
	Number   int         `json:"number"`
	URL      string      `json:"html_url"`
	State    string      `json:"state"`
	Merged   bool        `json:"merged"`
	MergeSHA string      `json:"merge_commit_sha"`
	Base     externalRef `json:"base"`
	Head     externalRef `json:"head"`
}
type externalRef struct {
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	Repo struct {
		ID   int64  `json:"id"`
		Name string `json:"full_name"`
	} `json:"repo"`
}

func (p GitHubPublisher) externalPull(ctx context.Context, repository string, number int) (externalPull, error) {
	var pr externalPull
	data, err := p.command(ctx, "gh", "api", fmt.Sprintf("repos/%s/pulls/%d", repository, number))
	if err != nil {
		return pr, err
	}
	if err := json.Unmarshal(data, &pr); err != nil || pr.Number != number {
		return pr, fmt.Errorf("invalid external PR response")
	}
	return pr, nil
}

// ObserveExternalPR reads both PRs and, after merging, the merge commit itself.
func (p GitHubPublisher) ObserveExternalPR(ctx context.Context, plan Plan, publication Publication, number int) (ExternalPRObservation, error) {
	var o ExternalPRObservation
	if p.Frozen == nil || number < 1 || publication.Number < 1 || !githubRepository.MatchString(plan.Repository) {
		return o, fmt.Errorf("invalid external PR request")
	}
	if err := p.Frozen.Validate(); err != nil {
		return o, err
	}
	if err := p.verifyIdentity(ctx); err != nil {
		return o, err
	}
	if err := p.verifyRepositoryIdentity(ctx, plan); err != nil {
		return o, err
	}
	parent, err := p.externalPull(ctx, plan.Repository, publication.Number)
	if err != nil {
		return o, err
	}
	incoming, err := p.externalPull(ctx, plan.Repository, number)
	if err != nil {
		return o, err
	}
	o.Boundary = ExternalPRBoundary{Repository: plan.Repository, RepositoryID: parent.Base.Repo.ID, ParentNumber: parent.Number, ParentURL: parent.URL, ParentBase: parent.Base.Ref, ParentBaseSHA: parent.Base.SHA, ParentBranch: parent.Head.Ref, ParentHeadSHA: parent.Head.SHA, IncomingNumber: incoming.Number, IncomingURL: incoming.URL, IncomingHeadRepository: incoming.Head.Repo.Name, IncomingHeadRepositoryID: incoming.Head.Repo.ID, IncomingHeadBranch: incoming.Head.Ref, IncomingHeadSHA: incoming.Head.SHA}
	if parent.Base.Repo.Name != plan.Repository || parent.Base.Repo.ID != p.Frozen.RepositoryID || parent.Head.Repo.Name != plan.Repository || parent.Head.Repo.ID != parent.Base.Repo.ID {
		return o, fmt.Errorf("parent PR repository changed")
	}
	o.ParentState = "CLOSED"
	if parent.State == "open" {
		o.ParentState = "OPEN"
	}
	o.IncomingState = "CLOSED"
	if incoming.State == "open" {
		o.IncomingState = "OPEN"
	}
	if incoming.Merged {
		o.IncomingState = "MERGED"
	}
	o.IncomingBaseRepositoryID, o.IncomingBaseBranch, o.IncomingBaseSHA = incoming.Base.Repo.ID, incoming.Base.Ref, incoming.Base.SHA
	if incoming.Base.Repo.Name != plan.Repository {
		return o, fmt.Errorf("incoming PR repository changed")
	}
	if !objectID.MatchString(parent.Head.SHA) {
		return o, fmt.Errorf("invalid parent PR head")
	}
	data, err := p.command(ctx, "gh", "api", fmt.Sprintf("repos/%s/git/commits/%s", plan.Repository, parent.Head.SHA))
	if err != nil {
		return o, err
	}
	var headCommit struct {
		SHA  string `json:"sha"`
		Tree struct {
			SHA string `json:"sha"`
		} `json:"tree"`
	}
	if json.Unmarshal(data, &headCommit) != nil || headCommit.SHA != parent.Head.SHA || !objectID.MatchString(headCommit.Tree.SHA) {
		return o, fmt.Errorf("invalid parent commit tree")
	}
	o.ParentTree = headCommit.Tree.SHA
	if incoming.Merged {
		o.MergeSHA = incoming.MergeSHA
		if !objectID.MatchString(o.MergeSHA) {
			return o, fmt.Errorf("invalid merge revision")
		}
		data, err := p.command(ctx, "gh", "api", fmt.Sprintf("repos/%s/commits/%s", plan.Repository, o.MergeSHA))
		if err != nil {
			return o, err
		}
		var commit struct {
			SHA     string `json:"sha"`
			Parents []struct {
				SHA string `json:"sha"`
			} `json:"parents"`
		}
		if json.Unmarshal(data, &commit) != nil || commit.SHA != o.MergeSHA {
			return o, fmt.Errorf("invalid merge commit response")
		}
		for _, parent := range commit.Parents {
			o.MergeParents = append(o.MergeParents, parent.SHA)
		}
	}
	return o, nil
}

func (p GitHubPublisher) MergeExternalPR(ctx context.Context, plan Plan, boundary ExternalPRBoundary) error {
	observed, err := p.ObserveExternalPR(ctx, plan, Publication{Number: boundary.ParentNumber}, boundary.IncomingNumber)
	if err != nil {
		return err
	}
	if err := ValidateExternalPRBeforeMerge(boundary, observed); err != nil {
		return err
	}
	_, err = p.command(ctx, "gh", "api", "--method", "PUT", fmt.Sprintf("repos/%s/pulls/%d/merge", plan.Repository, boundary.IncomingNumber), "-f", "sha="+boundary.IncomingHeadSHA, "-f", "merge_method=merge")
	return err
}

func (p DockerPublisher) ObserveExternalPR(ctx context.Context, plan Plan, publication Publication, number int) (o ExternalPRObservation, err error) {
	err = p.withFeedbackPublisher(ctx, plan, func(g GitHubPublisher) error {
		var e error
		o, e = g.ObserveExternalPR(ctx, plan, publication, number)
		return e
	})
	return
}
func (p DockerPublisher) MergeExternalPR(ctx context.Context, plan Plan, boundary ExternalPRBoundary) error {
	return p.withFeedbackPublisher(ctx, plan, func(g GitHubPublisher) error { return g.MergeExternalPR(ctx, plan, boundary) })
}

// UpdateExternalPRMetadata is idempotent after an uncertain request. It
// validates the exact merged head on both sides of the metadata update.
func (p GitHubPublisher) UpdateExternalPRMetadata(ctx context.Context, plan Plan, publication Publication, boundary ExternalPRBoundary) error {
	if err := ValidatePRMetadata(plan); err != nil {
		return err
	}
	check := func() error {
		o, err := p.ObserveExternalPR(ctx, plan, publication, boundary.IncomingNumber)
		if err != nil {
			return err
		}
		return ValidateExternalPRMerge(boundary, o)
	}
	if err := check(); err != nil {
		return err
	}
	title := plan.PRTitle
	if !strings.Contains(title, plan.Reference) {
		title = plan.Reference + ": " + title
	}
	_, err := p.command(ctx, "gh", "api", "--method", "PATCH", fmt.Sprintf("repos/%s/pulls/%d", plan.Repository, publication.Number), "-f", "title="+title, "-f", "body="+plan.PRBody)
	if err != nil {
		return err
	}
	return check()
}

func (p DockerPublisher) UpdateExternalPRMetadata(ctx context.Context, plan Plan, publication Publication, boundary ExternalPRBoundary) error {
	return p.withFeedbackPublisher(ctx, plan, func(g GitHubPublisher) error { return g.UpdateExternalPRMetadata(ctx, plan, publication, boundary) })
}
