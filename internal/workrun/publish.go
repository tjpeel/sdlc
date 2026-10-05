package workrun

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type Command func(context.Context, string, ...string) ([]byte, error)

func localCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, name, args...)
	// Inherited Git routing can redirect a supposedly disposable repository.
	for _, entry := range os.Environ() {
		key := strings.SplitN(entry, "=", 2)[0]
		if !strings.HasPrefix(key, "GIT_") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "GIT_TERMINAL_PROMPT=0", "GH_PROMPT_DISABLED=1", "GIT_OPTIONAL_LOCKS=0")
	var diagnostic bytes.Buffer
	command.Stderr = &diagnostic
	data, err := command.Output()
	if err != nil {
		return data, fmt.Errorf("%s command failed; check host authentication, signing and remote state", name)
	}
	return data, nil
}

// GitHubPublisher receives an exported bundle, never the worker's .git config.
// Its repository and signing/publication credentials are not worker mounts.
type GitHubPublisher struct {
	Command                            Command
	Frozen                             *PublicationIdentity
	ExpectedHead, ExpectedTree         string
	SigningKeyPath, AllowedSignersPath string
	BundlePath                         string
	AfterSigning                       func() error
}

// Snapshot reads one canonical GitHub branch into the publisher's private
// directory. It never consults a worker checkout or host Git configuration.
func (publisher GitHubPublisher) Snapshot(ctx context.Context, plan Plan, branch, expectedSHA, directory string) (BranchSnapshot, error) {
	if publisher.Frozen == nil || !githubRepository.MatchString(plan.Repository) {
		return BranchSnapshot{}, fmt.Errorf("snapshot requires frozen repository identity")
	}
	if err := publisher.Frozen.Validate(); err != nil {
		return BranchSnapshot{}, err
	}
	if err := publisher.verifyIdentity(ctx); err != nil {
		return BranchSnapshot{}, err
	}
	if err := publisher.verifyRepositoryIdentity(ctx, plan); err != nil {
		return BranchSnapshot{}, err
	}
	if _, err := publisher.command(ctx, "gh", "auth", "status", "--hostname", "github.com"); err != nil {
		return BranchSnapshot{}, err
	}
	if _, err := publisher.command(ctx, "git", "check-ref-format", "--branch", branch); err != nil {
		return BranchSnapshot{}, fmt.Errorf("invalid snapshot branch")
	}
	data, err := publisher.command(ctx, "gh", "api", "repos/"+plan.Repository+"/branches/"+url.PathEscape(branch))
	if err != nil {
		return BranchSnapshot{}, err
	}
	var remote struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if json.Unmarshal(data, &remote) != nil || !objectID.MatchString(remote.Commit.SHA) || expectedSHA != "" && remote.Commit.SHA != expectedSHA {
		return BranchSnapshot{}, fmt.Errorf("snapshot branch changed or is invalid")
	}
	local := filepath.Join(directory, "repository.git")
	if _, err = publisher.command(ctx, "git", "init", "--bare", local); err != nil {
		return BranchSnapshot{}, err
	}
	if err = realDirectory(local); err != nil {
		return BranchSnapshot{}, err
	}
	canonical := "https://github.com/" + plan.Repository + ".git"
	if _, err = publisher.git(ctx, local, "fetch", "--no-tags", canonical, "refs/heads/"+branch+":refs/sdlc/snapshot"); err != nil {
		return BranchSnapshot{}, err
	}
	head, err := publisher.git(ctx, local, "rev-parse", "refs/sdlc/snapshot")
	if err != nil || head != remote.Commit.SHA {
		return BranchSnapshot{}, fmt.Errorf("snapshot did not retain expected branch revision")
	}
	bundle := filepath.Join(directory, "snapshot.bundle")
	if _, err = publisher.git(ctx, local, "bundle", "create", bundle, "refs/sdlc/snapshot"); err != nil {
		return BranchSnapshot{}, err
	}
	if err = validateSnapshotBundle(bundle); err != nil {
		return BranchSnapshot{}, err
	}
	return BranchSnapshot{Branch: branch, SHA: head}, nil
}

func (publisher GitHubPublisher) Branch(ctx context.Context, plan Plan, branch string) (BranchSnapshot, error) {
	if publisher.Frozen == nil || !githubRepository.MatchString(plan.Repository) {
		return BranchSnapshot{}, fmt.Errorf("branch observation requires frozen repository identity")
	}
	if err := publisher.Frozen.Validate(); err != nil {
		return BranchSnapshot{}, err
	}
	if err := publisher.verifyIdentity(ctx); err != nil {
		return BranchSnapshot{}, err
	}
	if err := publisher.verifyRepositoryIdentity(ctx, plan); err != nil {
		return BranchSnapshot{}, err
	}
	if _, err := publisher.command(ctx, "gh", "auth", "status", "--hostname", "github.com"); err != nil {
		return BranchSnapshot{}, err
	}
	if _, err := publisher.command(ctx, "git", "check-ref-format", "--branch", branch); err != nil {
		return BranchSnapshot{}, fmt.Errorf("invalid snapshot branch")
	}
	data, err := publisher.command(ctx, "gh", "api", "repos/"+plan.Repository+"/branches/"+url.PathEscape(branch))
	if err != nil {
		return BranchSnapshot{}, err
	}
	var remote struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if json.Unmarshal(data, &remote) != nil || !objectID.MatchString(remote.Commit.SHA) {
		return BranchSnapshot{}, fmt.Errorf("branch revision is invalid")
	}
	return BranchSnapshot{Branch: branch, SHA: remote.Commit.SHA}, nil
}

func (publisher GitHubPublisher) Observe(ctx context.Context, plan Plan, publication Publication) (RemotePR, error) {
	if publisher.Frozen == nil || publication.Number < 1 || !githubRepository.MatchString(plan.Repository) {
		return RemotePR{}, fmt.Errorf("observation requires frozen publication identity")
	}
	if err := publisher.Frozen.Validate(); err != nil {
		return RemotePR{}, err
	}
	if err := publisher.verifyIdentity(ctx); err != nil {
		return RemotePR{}, err
	}
	if err := publisher.verifyRepositoryIdentity(ctx, plan); err != nil {
		return RemotePR{}, err
	}
	data, err := publisher.command(ctx, "gh", "pr", "view", fmt.Sprint(publication.Number), "--repo", plan.Repository, "--json", "url,state,baseRefName,baseRefOid,headRefName,headRefOid,mergeCommit")
	if err != nil {
		return RemotePR{}, err
	}
	var pr struct {
		URL     string `json:"url"`
		State   string `json:"state"`
		Base    string `json:"baseRefName"`
		BaseSHA string `json:"baseRefOid"`
		Head    string `json:"headRefName"`
		HeadSHA string `json:"headRefOid"`
		Merge   struct {
			SHA string `json:"oid"`
		} `json:"mergeCommit"`
	}
	if json.Unmarshal(data, &pr) != nil || !strings.HasPrefix(pr.URL, "https://github.com/"+plan.Repository+"/pull/") || pr.Head != plan.Branch || (pr.State != "OPEN" && pr.State != "MERGED" && pr.State != "CLOSED") || !objectID.MatchString(pr.HeadSHA) {
		return RemotePR{}, fmt.Errorf("PR identity changed externally")
	}
	if pr.BaseSHA != "" && !objectID.MatchString(pr.BaseSHA) || pr.Merge.SHA != "" && !objectID.MatchString(pr.Merge.SHA) {
		return RemotePR{}, fmt.Errorf("invalid observed PR boundary")
	}
	return RemotePR{State: pr.State, Base: pr.Base, BaseSHA: pr.BaseSHA, HeadSHA: pr.HeadSHA, MergeSHA: pr.Merge.SHA}, nil
}

func (publisher GitHubPublisher) command(ctx context.Context, name string, args ...string) ([]byte, error) {
	run := publisher.Command
	if run == nil {
		run = localCommand
	}
	return run(ctx, name, args...)
}

func (publisher GitHubPublisher) git(ctx context.Context, directory string, args ...string) (string, error) {
	helper := "!gh auth git-credential"
	if publisher.Frozen != nil {
		helper = "!/usr/local/bin/gh auth git-credential"
	}
	base := []string{"-C", directory, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.pager=cat", "-c", "credential.helper=", "-c", "http.followRedirects=false", "-c", "credential.https://github.com.helper=" + helper}
	data, err := publisher.command(ctx, "git", append(base, args...)...)
	return strings.TrimSpace(string(data)), err
}

var githubRepository = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func (publisher GitHubPublisher) Publish(ctx context.Context, plan Plan, workspace, directory string, previous Publication, output io.Writer) (Publication, error) {
	if !githubRepository.MatchString(plan.Repository) || !objectID.MatchString(plan.BaseSHA) {
		return Publication{}, fmt.Errorf("publication requires an explicit GitHub repository and base revision")
	}
	if publisher.Frozen != nil {
		if err := publisher.Frozen.Validate(); err != nil {
			return Publication{}, err
		}
		if !objectID.MatchString(plan.SourceSHA) || !objectID.MatchString(publisher.ExpectedHead) || !objectID.MatchString(publisher.ExpectedTree) || publisher.SigningKeyPath == "" || publisher.AllowedSignersPath == "" {
			return Publication{}, fmt.Errorf("frozen publication requires tested revisions and signing")
		}
		if err := publisher.verifyIdentity(ctx); err != nil {
			return Publication{}, err
		}
		if plan.Restack == nil {
			if err := publisher.verifyRemoteBoundary(ctx, plan); err != nil {
				return Publication{}, err
			}
		} else if err := publisher.verifyRepositoryIdentity(ctx, plan); err != nil {
			return Publication{}, err
		}
	}
	if _, err := publisher.command(ctx, "gh", "auth", "status", "--hostname", "github.com"); err != nil {
		return Publication{}, err
	}
	if _, err := publisher.command(ctx, "git", "check-ref-format", "--branch", plan.Branch); err != nil {
		return Publication{}, fmt.Errorf("invalid destination branch")
	}
	if _, err := publisher.command(ctx, "git", "check-ref-format", "--branch", plan.Base); err != nil {
		return Publication{}, fmt.Errorf("invalid PR base branch")
	}
	baseData, err := publisher.command(ctx, "gh", "api", "repos/"+plan.Repository+"/branches/"+url.PathEscape(plan.Base))
	if err != nil {
		return Publication{}, err
	}
	var remoteBase struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if json.Unmarshal(baseData, &remoteBase) != nil || remoteBase.Commit.SHA != plan.BaseSHA {
		return Publication{}, fmt.Errorf("remote base changed or differs from the captured local base; reconcile before publishing")
	}
	local := filepath.Join(directory, "publication.git")
	if _, err := os.Lstat(local); os.IsNotExist(err) {
		if _, err := publisher.command(ctx, "git", "init", "--bare", local); err != nil {
			return Publication{}, err
		}
	} else if err != nil {
		return Publication{}, err
	}
	if err := realDirectory(local); err != nil {
		return Publication{}, err
	}
	bundle := publisher.BundlePath
	if bundle == "" {
		bundle = filepath.Join(directory, "source.bundle")
	}
	if publisher.Frozen != nil {
		if _, err := publisher.git(ctx, local, "bundle", "verify", bundle); err != nil {
			return Publication{}, fmt.Errorf("exported bundle verification failed")
		}
	}
	if _, err := publisher.git(ctx, local, "-c", "protocol.file.allow=always", "fetch", "--no-tags", bundle, "HEAD:refs/sdlc/candidate"); err != nil {
		return Publication{}, err
	}
	candidate, err := publisher.git(ctx, local, "rev-parse", "refs/sdlc/candidate")
	if err != nil || !objectID.MatchString(candidate) {
		return Publication{}, fmt.Errorf("cannot identify exported candidate")
	}
	if publisher.Frozen != nil {
		tree, err := publisher.git(ctx, local, "rev-parse", candidate+"^{tree}")
		if err != nil || candidate != publisher.ExpectedHead || tree != publisher.ExpectedTree {
			return Publication{}, fmt.Errorf("exported candidate differs from tested revision")
		}
		if _, err := publisher.git(ctx, local, "fsck", "--strict", "--no-reflogs"); err != nil {
			return Publication{}, fmt.Errorf("exported objects failed integrity checks")
		}
	}
	if _, err := publisher.git(ctx, local, "merge-base", "--is-ancestor", plan.SourceSHA, candidate); err != nil {
		return Publication{}, fmt.Errorf("implementation no longer descends from its original source")
	}
	commits, err := publisher.git(ctx, local, "rev-list", "--reverse", "--first-parent", plan.SourceSHA+".."+candidate)
	if err != nil || commits == "" {
		return Publication{}, fmt.Errorf("ticket produced no implementation commit")
	}
	mapping := map[string]string{}
	mapPath := filepath.Join(directory, "signed-commits.json")
	if data, err := os.ReadFile(mapPath); err == nil {
		if json.Unmarshal(data, &mapping) != nil {
			return Publication{}, fmt.Errorf("invalid retained signing journal")
		}
	} else if !os.IsNotExist(err) {
		return Publication{}, err
	}
	settings := []string{}
	if publisher.Frozen != nil {
		settings = []string{"-c", "user.name=" + publisher.Frozen.GitName, "-c", "user.email=" + publisher.Frozen.GitEmail, "-c", "commit.gpgsign=true", "-c", "gpg.format=ssh", "-c", "gpg.ssh.program=/usr/bin/ssh-keygen", "-c", "gpg.ssh.allowedSignersFile=" + publisher.AllowedSignersPath, "-c", "user.signingKey=" + publisher.SigningKeyPath}
	} else {
		for _, key := range []string{"user.name", "user.email", "commit.gpgsign", "gpg.format", "gpg.program", "gpg.ssh.program", "gpg.ssh.allowedSignersFile", "user.signingKey"} {
			args := []string{"config", "--get", key}
			if key == "commit.gpgsign" {
				args = []string{"config", "--type=bool", "--get", key}
			}
			value, err := publisher.git(ctx, plan.Root, args...)
			if err == nil && value != "" {
				if strings.ContainsAny(value, "\x00\r\n") {
					return Publication{}, fmt.Errorf("unsafe host signing setting")
				}
				settings = append(settings, "-c", key+"="+value)
			}
		}
	}
	parent := plan.SourceSHA
	signing := false
	for i := 0; i+1 < len(settings); i += 2 {
		if settings[i+1] == "commit.gpgsign=true" {
			signing = true
		}
	}
	originalParent := plan.SourceSHA
	for _, commit := range strings.Fields(commits) {
		parents, err := publisher.git(ctx, local, "show", "-s", "--format=%P", commit)
		if err != nil || parents != originalParent {
			return Publication{}, fmt.Errorf("ticket publication currently requires linear implementation commits")
		}
		if signed := mapping[commit]; signed != "" {
			if !objectID.MatchString(signed) {
				return Publication{}, fmt.Errorf("invalid retained signed revision")
			}
			if publisher.Frozen != nil {
				originalTree, err := publisher.git(ctx, local, "show", "-s", "--format=%T", commit)
				signedTree, treeErr := publisher.git(ctx, local, "show", "-s", "--format=%T", signed)
				signedParent, parentErr := publisher.git(ctx, local, "show", "-s", "--format=%P", signed)
				if err != nil || treeErr != nil || parentErr != nil || originalTree != signedTree || signedParent != parent {
					return Publication{}, fmt.Errorf("retained signed commit differs from exported history")
				}
			}
			if signing {
				if _, err := publisher.git(ctx, local, append(append([]string{}, settings...), "verify-commit", signed)...); err != nil {
					return Publication{}, fmt.Errorf("retained controller signature could not be verified")
				}
			}
			parent = signed
			originalParent = commit
			continue
		}
		tree, err := publisher.git(ctx, local, "show", "-s", "--format=%T", commit)
		if err != nil {
			return Publication{}, err
		}
		message, err := publisher.git(ctx, local, "show", "-s", "--format=%B", commit)
		if err != nil || message == "" {
			return Publication{}, fmt.Errorf("implementation commit has no message")
		}
		args := append(append([]string{}, settings...), "commit-tree", tree, "-p", parent, "-m", message)
		// commit-tree does not honour commit.gpgsign implicitly.
		if signing {
			args = append(args, "-S")
		}
		signed, err := publisher.git(ctx, local, args...)
		if err != nil || !objectID.MatchString(signed) {
			return Publication{}, fmt.Errorf("controller commit/signing failed; original worker commits are retained")
		}
		if signing {
			if _, err := publisher.git(ctx, local, append(append([]string{}, settings...), "verify-commit", signed)...); err != nil {
				return Publication{}, fmt.Errorf("controller signature could not be verified")
			}
		}
		mapping[commit] = signed
		parent = signed
		originalParent = commit
	}
	if err := saveJSON(mapPath, mapping); err != nil {
		return Publication{}, err
	}
	if _, err := publisher.git(ctx, local, "update-ref", "refs/heads/publication", parent); err != nil {
		return Publication{}, err
	}
	workerTree, err := publisher.git(ctx, local, "rev-parse", candidate+"^{tree}")
	if err != nil {
		return Publication{}, err
	}
	publishedTree, err := publisher.git(ctx, local, "rev-parse", parent+"^{tree}")
	if err != nil || workerTree != publishedTree {
		return Publication{}, fmt.Errorf("publication tree differs from verified implementation")
	}
	if publisher.AfterSigning != nil {
		if err := publisher.AfterSigning(); err != nil {
			return Publication{}, fmt.Errorf("signing credential cleanup failed before publication")
		}
	}
	prs, err := publisher.command(ctx, "gh", "pr", "list", "--repo", plan.Repository, "--head", plan.Branch, "--state", "all", "--json", "number,url,baseRefName,baseRefOid,headRefOid,state")
	if err != nil {
		return Publication{}, err
	}
	var matches []struct {
		Number  int    `json:"number"`
		URL     string `json:"url"`
		Base    string `json:"baseRefName"`
		BaseSHA string `json:"baseRefOid"`
		Head    string `json:"headRefOid"`
		State   string `json:"state"`
	}
	if json.Unmarshal(prs, &matches) != nil || len(matches) > 1 {
		return Publication{}, fmt.Errorf("cannot reconcile destination PR")
	}
	if len(matches) == 1 {
		pr := matches[0]
		if pr.State != "OPEN" || (previous.Number != 0 && previous.Number != pr.Number) {
			return Publication{}, fmt.Errorf("destination PR was changed or closed externally")
		}
		if plan.Restack == nil && pr.Base != plan.Base {
			return Publication{}, fmt.Errorf("destination PR was changed or closed externally")
		}
		if plan.Restack != nil && (pr.Number != plan.Restack.Number || !validRestackPR(plan, pr.Base, pr.BaseSHA, pr.Head, parent)) {
			return Publication{}, fmt.Errorf("destination PR was changed externally; refusing retarget")
		}
		if pr.Head != parent && (previous.HeadSHA == "" || pr.Head != previous.HeadSHA) {
			return Publication{}, fmt.Errorf("PR head changed externally; refusing to overwrite it")
		}
	}
	remote, err := publisher.git(ctx, local, "ls-remote", "https://github.com/"+plan.Repository+".git", "refs/heads/"+plan.Branch)
	if err != nil {
		return Publication{}, err
	}
	remoteHead := ""
	if fields := strings.Fields(remote); len(fields) > 0 {
		remoteHead = fields[0]
	}
	if remoteHead != "" && remoteHead != parent && remoteHead != previous.HeadSHA {
		return Publication{}, fmt.Errorf("destination branch changed externally; refusing publication")
	}
	if publisher.Frozen != nil {
		if err := publisher.verifyIdentity(ctx); err != nil {
			return Publication{}, err
		}
		if plan.Restack == nil {
			if err := publisher.verifyRemoteBoundary(ctx, plan); err != nil {
				return Publication{}, err
			}
		} else if err := publisher.verifyRestackBoundary(ctx, plan, parent); err != nil {
			return Publication{}, err
		}
	}
	if remoteHead != parent {
		// An exact lease also rejects a competing fast-forward between the
		// last inspection and push. An empty expected value requires absence.
		lease := "--force-with-lease=refs/heads/" + plan.Branch + ":" + remoteHead
		if _, err := publisher.git(ctx, local, "push", "--porcelain", lease, "https://github.com/"+plan.Repository+".git", parent+":refs/heads/"+plan.Branch); err != nil {
			return Publication{}, err
		}
	}
	confirmed, err := publisher.git(ctx, local, "ls-remote", "https://github.com/"+plan.Repository+".git", "refs/heads/"+plan.Branch)
	if err != nil || !strings.HasPrefix(confirmed, parent+"\t") {
		return Publication{}, fmt.Errorf("remote branch publication was not confirmed")
	}
	body := plan.PRBody + fmt.Sprintf("\n\nWork reference: %s\n\nConfigured repository checks passed for tree %s. Independent review is coordinated by SDLC.\n", plan.Reference, workerTree)
	bodyPath := filepath.Join(directory, "pr-body.md")
	if err := os.WriteFile(bodyPath, []byte(body), 0600); err != nil {
		return Publication{}, err
	}
	title := plan.PRTitle
	if !strings.Contains(title, plan.Reference) {
		title = plan.Reference + ": " + title
	}
	if len(matches) == 0 {
		if plan.Restack != nil {
			return Publication{}, fmt.Errorf("restack destination PR is missing")
		}
		if publisher.Frozen != nil {
			if err := publisher.verifyRemoteBoundary(ctx, plan); err != nil {
				return Publication{}, err
			}
		}
		if _, err := publisher.command(ctx, "gh", "pr", "create", "--repo", plan.Repository, "--head", plan.Branch, "--base", plan.Base, "--draft", "--title", title, "--body-file", bodyPath); err != nil {
			return Publication{}, err
		}
	} else {
		if publisher.Frozen != nil {
			if plan.Restack == nil {
				if err := publisher.verifyRemoteBoundary(ctx, plan); err != nil {
					return Publication{}, err
				}
			} else if err := publisher.verifyRestackBoundary(ctx, plan, parent); err != nil {
				return Publication{}, err
			}
		}
		if plan.Restack != nil {
			if _, err := publisher.command(ctx, "gh", "pr", "edit", fmt.Sprint(matches[0].Number), "--repo", plan.Repository, "--base", plan.Base); err != nil {
				return Publication{}, err
			}
			if publisher.Frozen != nil {
				if err := publisher.verifyRemoteBoundary(ctx, plan); err != nil {
					return Publication{}, err
				}
			}
		}
		if _, err := publisher.command(ctx, "gh", "pr", "edit", fmt.Sprint(matches[0].Number), "--repo", plan.Repository, "--title", title, "--body-file", bodyPath); err != nil {
			return Publication{}, err
		}
	}
	data, err := publisher.command(ctx, "gh", "pr", "view", plan.Branch, "--repo", plan.Repository, "--json", "number,url,baseRefOid,headRefOid")
	if err != nil {
		return Publication{}, err
	}
	var result struct {
		Number int    `json:"number"`
		URL    string `json:"url"`
		Base   string `json:"baseRefOid"`
		Head   string `json:"headRefOid"`
	}
	if json.Unmarshal(data, &result) != nil || result.Number < 1 || result.Base != plan.BaseSHA || result.Head != parent || !strings.HasPrefix(result.URL, "https://github.com/"+plan.Repository+"/pull/") {
		return Publication{}, fmt.Errorf("published PR revision could not be confirmed")
	}
	if output != nil {
		fmt.Fprintln(output, "Draft PR:", result.URL)
	}
	return Publication{result.URL, result.Number, result.Base, result.Head}, nil
}

func (publisher GitHubPublisher) Checks(ctx context.Context, plan Plan, pr Publication) (CIResult, error) {
	if !githubRepository.MatchString(plan.Repository) || pr.Number < 1 || !objectID.MatchString(pr.BaseSHA) || !objectID.MatchString(pr.HeadSHA) {
		return CIResult{}, fmt.Errorf("CI inspection requires exact publication identity")
	}
	if publisher.Frozen != nil {
		if err := publisher.Frozen.Validate(); err != nil {
			return CIResult{}, err
		}
		if err := publisher.verifyIdentity(ctx); err != nil {
			return CIResult{}, err
		}
		if err := publisher.verifyRemoteBoundary(ctx, plan); err != nil {
			return CIResult{}, err
		}
	}
	data, err := publisher.command(ctx, "gh", "pr", "view", fmt.Sprint(pr.Number), "--repo", plan.Repository, "--json", "baseRefOid,headRefOid,state")
	if err != nil {
		return CIResult{}, err
	}
	var revision struct {
		Base  string `json:"baseRefOid"`
		Head  string `json:"headRefOid"`
		State string `json:"state"`
	}
	if json.Unmarshal(data, &revision) != nil || revision.Base != pr.BaseSHA || revision.Head != pr.HeadSHA || revision.State != "OPEN" {
		return CIResult{}, fmt.Errorf("PR revision changed while waiting for CI")
	}
	data, checkErr := publisher.command(ctx, "gh", "pr", "checks", fmt.Sprint(pr.Number), "--repo", plan.Repository, "--json", "name,bucket,state,link")
	// gh exits nonzero for pending/failed checks; parse its structured output.
	_ = checkErr
	var checks []struct {
		Name   string `json:"name"`
		Bucket string `json:"bucket"`
		State  string `json:"state"`
		Link   string `json:"link"`
	}
	if json.Unmarshal(data, &checks) != nil || checks == nil {
		return CIResult{}, fmt.Errorf("required CI checks could not be read")
	}
	result := CIResult{Status: "passed", Details: string(data)}
	if len(checks) == 0 {
		result.Status = "missing"
	}
	for _, check := range checks {
		if check.Name == "" {
			return CIResult{}, fmt.Errorf("required check has no identity")
		}
		switch check.Bucket {
		case "pass":
		case "pending":
			if result.Status == "passed" {
				result.Status = "pending"
			}
		case "fail", "cancel", "skipping":
			result.Status = "failed"
		default:
			return CIResult{}, fmt.Errorf("required check has unknown status")
		}
	}
	// Re-read the boundary after querying checks so results never certify a
	// concurrently changed PR head or base.
	after, err := publisher.command(ctx, "gh", "pr", "view", fmt.Sprint(pr.Number), "--repo", plan.Repository, "--json", "baseRefOid,headRefOid,state")
	if err != nil {
		return CIResult{}, err
	}
	var confirmed struct {
		Base  string `json:"baseRefOid"`
		Head  string `json:"headRefOid"`
		State string `json:"state"`
	}
	if json.Unmarshal(after, &confirmed) != nil || confirmed.Base != pr.BaseSHA || confirmed.Head != pr.HeadSHA || confirmed.State != "OPEN" {
		return CIResult{}, fmt.Errorf("PR revision changed during CI inspection")
	}
	if publisher.Frozen != nil {
		if err := publisher.verifyRemoteBoundary(ctx, plan); err != nil {
			return CIResult{}, err
		}
	}
	return result, nil
}

func (identity PublicationIdentity) Validate() error {
	if identity.RepositoryID <= 0 || !githubRepository.MatchString(identity.RepositoryName) || identity.ProfileID == "" || identity.GitHubID <= 0 || !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*$`).MatchString(identity.GitHubLogin) || identity.GitName == "" || identity.GitEmail == "" || !strings.HasPrefix(identity.SSHPublicKey, "ssh-") || !strings.HasPrefix(identity.SSHFingerprint, "SHA256:") {
		return fmt.Errorf("frozen publication identity is incomplete")
	}
	for _, value := range []string{identity.ProfileID, identity.GitName, identity.GitEmail, identity.SSHPublicKey, identity.SSHFingerprint} {
		if strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("frozen publication identity is invalid")
		}
	}
	return nil
}

func (publisher GitHubPublisher) verifyRemoteBoundary(ctx context.Context, plan Plan) error {
	if err := publisher.verifyRepositoryIdentity(ctx, plan); err != nil {
		return err
	}
	data, err := publisher.command(ctx, "gh", "api", "repos/"+plan.Repository+"/branches/"+url.PathEscape(plan.Base))
	var base struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err != nil || json.Unmarshal(data, &base) != nil || base.Commit.SHA != plan.BaseSHA {
		return fmt.Errorf("remote base changed; refusing publication")
	}
	return nil
}

func (publisher GitHubPublisher) verifyRepositoryIdentity(ctx context.Context, plan Plan) error {
	data, err := publisher.command(ctx, "gh", "api", "repos/"+plan.Repository)
	var repository struct {
		ID   int64  `json:"id"`
		Name string `json:"full_name"`
	}
	if err != nil || json.Unmarshal(data, &repository) != nil || repository.ID != publisher.Frozen.RepositoryID || repository.Name != publisher.Frozen.RepositoryName || repository.Name != plan.Repository {
		return fmt.Errorf("repository identity changed; refusing publication")
	}
	return nil
}

func (publisher GitHubPublisher) verifyRestackBoundary(ctx context.Context, plan Plan, candidate string) error {
	boundary := plan.Restack
	if boundary == nil || boundary.Number < 1 || boundary.Base == "" || !objectID.MatchString(boundary.BaseSHA) || !objectID.MatchString(boundary.HeadSHA) {
		return fmt.Errorf("restack requires a retained PR boundary")
	}
	// A retarget must never use a base that moved after the snapshot used for
	// the offline rebase. This is checked independently of the old PR base.
	if err := publisher.verifyTargetBase(ctx, plan); err != nil {
		return err
	}
	data, err := publisher.command(ctx, "gh", "pr", "view", fmt.Sprint(boundary.Number), "--repo", plan.Repository, "--json", "state,baseRefName,baseRefOid,headRefName,headRefOid")
	var pr struct {
		State   string `json:"state"`
		Base    string `json:"baseRefName"`
		BaseSHA string `json:"baseRefOid"`
		Head    string `json:"headRefName"`
		HeadSHA string `json:"headRefOid"`
	}
	if err != nil || json.Unmarshal(data, &pr) != nil || pr.State != "OPEN" || pr.Head != plan.Branch || !validRestackPR(plan, pr.Base, pr.BaseSHA, pr.HeadSHA, candidate) {
		return fmt.Errorf("destination PR was changed externally; refusing retarget")
	}
	return nil
}

func validRestackPR(plan Plan, base, baseSHA, head, candidate string) bool {
	boundary := plan.Restack
	if boundary == nil {
		return false
	}
	// The desired-base form permits an authorized parent auto-retarget before
	// push (the recorded head) and replay after push (the signed candidate).
	return (base == boundary.Base && baseSHA == boundary.BaseSHA && (head == boundary.HeadSHA || head == candidate)) ||
		(base == plan.Base && baseSHA == plan.BaseSHA && (head == boundary.HeadSHA || head == candidate))
}

func (publisher GitHubPublisher) verifyTargetBase(ctx context.Context, plan Plan) error {
	data, err := publisher.command(ctx, "gh", "api", "repos/"+plan.Repository+"/branches/"+url.PathEscape(plan.Base))
	var base struct {
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	if err != nil || json.Unmarshal(data, &base) != nil || base.Commit.SHA != plan.BaseSHA {
		return fmt.Errorf("remote base changed; refusing publication")
	}
	return nil
}
func (publisher GitHubPublisher) verifyIdentity(ctx context.Context) error {
	data, err := publisher.command(ctx, "gh", "api", "user")
	var identity struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	if err != nil || json.Unmarshal(data, &identity) != nil || identity.ID != publisher.Frozen.GitHubID || identity.Login != publisher.Frozen.GitHubLogin {
		return fmt.Errorf("GitHub account differs from frozen publication identity")
	}
	return nil
}
