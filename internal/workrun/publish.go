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
type GitHubPublisher struct{ Command Command }

func (publisher GitHubPublisher) command(ctx context.Context, name string, args ...string) ([]byte, error) {
	run := publisher.Command
	if run == nil {
		run = localCommand
	}
	return run(ctx, name, args...)
}

func (publisher GitHubPublisher) git(ctx context.Context, directory string, args ...string) (string, error) {
	base := []string{"-C", directory, "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "core.pager=cat", "-c", "credential.helper=", "-c", "credential.https://github.com.helper=!gh auth git-credential"}
	data, err := publisher.command(ctx, "git", append(base, args...)...)
	return strings.TrimSpace(string(data)), err
}

var githubRepository = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*/[A-Za-z0-9][A-Za-z0-9_.-]*$`)

func (publisher GitHubPublisher) Publish(ctx context.Context, plan Plan, workspace, directory string, previous Publication, output io.Writer) (Publication, error) {
	if !githubRepository.MatchString(plan.Repository) || !objectID.MatchString(plan.BaseSHA) {
		return Publication{}, fmt.Errorf("publication requires an explicit GitHub repository and base revision")
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
	if _, err := publisher.git(ctx, local, "-c", "protocol.file.allow=always", "fetch", "--no-tags", filepath.Join(directory, "source.bundle"), "HEAD:refs/sdlc/candidate"); err != nil {
		return Publication{}, err
	}
	candidate, err := publisher.git(ctx, local, "rev-parse", "refs/sdlc/candidate")
	if err != nil || !objectID.MatchString(candidate) {
		return Publication{}, fmt.Errorf("cannot identify exported candidate")
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
	prs, err := publisher.command(ctx, "gh", "pr", "list", "--repo", plan.Repository, "--head", plan.Branch, "--state", "all", "--json", "number,url,baseRefName,headRefOid,state")
	if err != nil {
		return Publication{}, err
	}
	var matches []struct {
		Number int    `json:"number"`
		URL    string `json:"url"`
		Base   string `json:"baseRefName"`
		Head   string `json:"headRefOid"`
		State  string `json:"state"`
	}
	if json.Unmarshal(prs, &matches) != nil || len(matches) > 1 {
		return Publication{}, fmt.Errorf("cannot reconcile destination PR")
	}
	if len(matches) == 1 {
		pr := matches[0]
		if pr.State != "OPEN" || pr.Base != plan.Base || (previous.Number != 0 && previous.Number != pr.Number) {
			return Publication{}, fmt.Errorf("destination PR was changed or closed externally")
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
	if remoteHead != parent {
		if _, err := publisher.git(ctx, local, "push", "--porcelain", "https://github.com/"+plan.Repository+".git", parent+":refs/heads/"+plan.Branch); err != nil {
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
		if _, err := publisher.command(ctx, "gh", "pr", "create", "--repo", plan.Repository, "--head", plan.Branch, "--base", plan.Base, "--draft", "--title", title, "--body-file", bodyPath); err != nil {
			return Publication{}, err
		}
	} else {
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
	var checks []struct {
		Name   string `json:"name"`
		Bucket string `json:"bucket"`
		State  string `json:"state"`
		Link   string `json:"link"`
	}
	if json.Unmarshal(data, &checks) != nil || checks == nil {
		return CIResult{}, fmt.Errorf("required CI checks could not be read: %w", checkErr)
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
	return result, nil
}
