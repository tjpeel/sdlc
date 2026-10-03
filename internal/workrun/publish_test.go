package workrun

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGitHubChecksStatusAndExactRevision(t *testing.T) {
	for _, tc := range []struct{ name, bucket, want string }{{"pass", "pass", "passed"}, {"pending", "pending", "pending"}, {"failed", "fail", "failed"}, {"cancelled", "cancel", "failed"}, {"skipped", "skipping", "failed"}, {"unknown", "unrecognized", "error"}} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			pub := GitHubPublisher{Command: func(_ context.Context, name string, args ...string) ([]byte, error) {
				calls++
				if name != "gh" {
					t.Fatal("unexpected connected boundary")
				}
				if args[1] == "view" {
					return []byte(`{"baseRefOid":"` + testBase + `","headRefOid":"` + testHead + `","state":"OPEN"}`), nil
				}
				if strings.Contains(strings.Join(args, " "), "--required") {
					t.Fatal("omitted available CI evidence")
				}
				data, _ := json.Marshal([]map[string]string{{"name": "tests", "bucket": tc.bucket, "state": "COMPLETED"}})
				return data, errors.New("gh check status exit")
			}}
			result, err := pub.Checks(context.Background(), Plan{Repository: "example/project"}, Publication{Number: 1, BaseSHA: testBase, HeadSHA: testHead})
			if tc.want == "error" {
				if err == nil {
					t.Fatal("unknown check accepted")
				}
				return
			}
			if err != nil || result.Status != tc.want || calls != 3 {
				t.Fatalf("result=%+v calls=%d error=%v", result, calls, err)
			}
		})
	}
}
func TestGitHubChecksRejectChangedBoundary(t *testing.T) {
	for _, kind := range []string{"head before", "base before", "closed", "head after", "malformed checks", "null checks", "nil checks", "unnamed check"} {
		t.Run(kind, func(t *testing.T) {
			views := 0
			pub := GitHubPublisher{Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if args[1] == "checks" {
					switch kind {
					case "malformed checks":
						return []byte(`bad`), nil
					case "null checks":
						return []byte(`null`), nil
					case "nil checks":
						return nil, nil
					case "unnamed check":
						return []byte(`[{"bucket":"pass"}]`), nil
					}
					return []byte(`[{"name":"tests","bucket":"pass"}]`), nil
				}
				views++
				base, head, state := testBase, testHead, "OPEN"
				if kind == "head before" || kind == "head after" && views == 2 {
					head = testTree
				}
				if kind == "base before" {
					base = testTree
				}
				if kind == "closed" {
					state = "CLOSED"
				}
				data, _ := json.Marshal(map[string]string{"baseRefOid": base, "headRefOid": head, "state": state})
				return data, nil
			}}
			if _, err := pub.Checks(context.Background(), Plan{Repository: "example/project"}, Publication{Number: 1, BaseSHA: testBase, HeadSHA: testHead}); err == nil {
				t.Fatal("uncertified CI boundary accepted")
			}
		})
	}
}
func TestGitHubPublicationRejectsInvalidDestinationBeforeCommands(t *testing.T) {
	calls := 0
	pub := GitHubPublisher{Command: func(context.Context, string, ...string) ([]byte, error) { calls++; return nil, nil }}
	if _, err := pub.Publish(context.Background(), Plan{Repository: "../private", BaseSHA: testBase}, "", "", Publication{}, nil); err == nil || calls != 0 {
		t.Fatal("unsafe repository reached command boundary")
	}
}

// This fake models remote side effects, including a response lost after PR
// creation. Every command remains offline and unexpected commands fail closed.
type publicationFake struct {
	t                        *testing.T
	plan                     Plan
	directory                string
	remote                   string
	exists                   bool
	lostCreateResponse       bool
	failSigning              bool
	signing, pushes, creates int
	verifying                int
	failVerification         bool
}

const testSigned = "4444444444444444444444444444444444444444"

func (f *publicationFake) command(_ context.Context, name string, args ...string) ([]byte, error) {
	f.t.Helper()
	response := func(v string) ([]byte, error) { return []byte(v), nil }
	if name == "gh" {
		switch {
		case args[0] == "auth":
			return response("")
		case args[0] == "api":
			return response(`{"commit":{"sha":"` + f.plan.BaseSHA + `"}}`)
		case args[0] == "pr" && args[1] == "list":
			if f.exists {
				return response(`[{"number":1,"url":"https://github.com/example/project/pull/1","baseRefName":"main","headRefOid":"` + f.remote + `","state":"OPEN"}]`)
			}
			return response(`[]`)
		case args[0] == "pr" && args[1] == "create":
			f.creates++
			f.exists = true
			if !strings.Contains(strings.Join(args, " "), "--draft") {
				f.t.Fatal("created a non-draft PR")
			}
			if f.lostCreateResponse {
				f.lostCreateResponse = false
				return nil, errors.New("lost create response")
			}
			return response("")
		case args[0] == "pr" && args[1] == "edit":
			return response("")
		case args[0] == "pr" && args[1] == "view":
			return response(`{"number":1,"url":"https://github.com/example/project/pull/1","baseRefOid":"` + f.plan.BaseSHA + `","headRefOid":"` + f.remote + `"}`)
		}
	}
	if name == "git" {
		if args[0] == "check-ref-format" {
			return response("")
		}
		if args[0] == "init" {
			if err := os.Mkdir(args[2], 0700); err != nil {
				return nil, err
			}
			return response("")
		}
		rest := args[2:]
		for len(rest) >= 2 && rest[0] == "-c" {
			rest = rest[2:]
		}
		switch rest[0] {
		case "fetch", "merge-base", "update-ref":
			return response("")
		case "rev-parse":
			if rest[1] == "refs/sdlc/candidate" {
				return response(testHead)
			}
			return response(testTree)
		case "rev-list":
			return response(testHead)
		case "config":
			if rest[len(rest)-1] == "commit.gpgsign" {
				if !strings.Contains(strings.Join(rest, " "), "--type=bool") {
					f.t.Fatal("signing boolean not normalized (yes/1 would be ignored)")
				}
				return response("true")
			}
			if rest[len(rest)-1] == "gpg.ssh.allowedSignersFile" {
				return response(filepath.Join(f.directory, "allowed-signers"))
			}
			return nil, errors.New("unset setting")
		case "show":
			switch rest[2] {
			case "--format=%P":
				return response(f.plan.SourceSHA)
			case "--format=%T":
				return response(testTree)
			case "--format=%B":
				return response("Implement selected task")
			}
		case "commit-tree":
			f.signing++
			if !strings.Contains(strings.Join(rest, " "), "-S") {
				f.t.Fatal("configured signing not requested")
			}
			if f.failSigning {
				return nil, errors.New("signing unavailable")
			}
			return response(testSigned)
		case "verify-commit":
			f.verifying++
			if !strings.Contains(strings.Join(args, " "), "gpg.ssh.allowedSignersFile=") {
				f.t.Fatal("verification lost allowed signers setting")
			}
			if f.failVerification {
				return nil, errors.New("invalid signature")
			}
			return response("")
		case "ls-remote":
			if f.remote == "" {
				return response("")
			}
			return response(f.remote + "\trefs/heads/" + f.plan.Branch)
		case "push":
			f.pushes++
			f.remote = testSigned
			return response("")
		}
	}
	f.t.Fatalf("unexpected publication command: %s %v", name, args)
	return nil, errors.New("unexpected command")
}
func TestPublicationReconcilesLostCreateResponseWithoutDuplicatePushOrPR(t *testing.T) {
	dir, j := testRun(t)
	j.Plan.PRTitle = "TASK-1 selected work"
	j.Plan.PRBody = "Implements TASK-1."
	f := &publicationFake{t: t, plan: j.Plan, directory: dir, lostCreateResponse: true}
	pub := GitHubPublisher{Command: f.command}
	if _, err := pub.Publish(context.Background(), j.Plan, j.Workspace, dir, Publication{}, nil); err == nil {
		t.Fatal("expected lost response")
	}
	result, err := pub.Publish(context.Background(), j.Plan, j.Workspace, dir, Publication{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.HeadSHA != testSigned || f.signing != 1 || f.verifying != 2 || f.pushes != 1 || f.creates != 1 {
		t.Fatalf("duplicate effects: %+v signing=%d pushes=%d creates=%d", result, f.signing, f.pushes, f.creates)
	}
}
func TestPublicationSigningFailureCannotPush(t *testing.T) {
	dir, j := testRun(t)
	j.Plan.PRTitle = "TASK-1 selected work"
	j.Plan.PRBody = "Implements TASK-1."
	f := &publicationFake{t: t, plan: j.Plan, directory: dir, failSigning: true}
	pub := GitHubPublisher{Command: f.command}
	if _, err := pub.Publish(context.Background(), j.Plan, j.Workspace, dir, Publication{}, nil); err == nil || f.pushes != 0 || f.creates != 0 {
		t.Fatal("signing failure published candidate")
	}
	f.failSigning = false
	if _, err := pub.Publish(context.Background(), j.Plan, j.Workspace, dir, Publication{}, nil); err != nil {
		t.Fatal(err)
	}
	if f.pushes != 1 || f.creates != 1 {
		t.Fatal("retry duplicated publication")
	}
}
func TestPublicationRefusesExternalBranchChange(t *testing.T) {
	dir, j := testRun(t)
	j.Plan.PRTitle = "TASK-1 selected work"
	j.Plan.PRBody = "Implements TASK-1."
	f := &publicationFake{t: t, plan: j.Plan, directory: dir, remote: testTree}
	pub := GitHubPublisher{Command: f.command}
	if _, err := pub.Publish(context.Background(), j.Plan, j.Workspace, dir, Publication{}, nil); err == nil || f.pushes != 0 || f.creates != 0 {
		t.Fatal("external branch overwritten")
	}
}

func TestPublicationSignatureVerificationFailurePreventsPush(t *testing.T) {
	dir, j := testRun(t)
	f := &publicationFake{t: t, plan: j.Plan, directory: dir, failVerification: true}
	pub := GitHubPublisher{Command: f.command}
	if _, err := pub.Publish(context.Background(), j.Plan, j.Workspace, dir, Publication{}, nil); err == nil || f.signing != 1 || f.verifying != 1 || f.pushes != 0 || f.creates != 0 {
		t.Fatalf("unverified signature published: error=%v signing=%d verify=%d pushes=%d", err, f.signing, f.verifying, f.pushes)
	}
}

func TestPublicationNormalizesYesSigningSetting(t *testing.T) {
	dir, j := testRun(t)
	config := filepath.Join(dir, "host-signing-config")
	if err := os.WriteFile(config, []byte("[commit]\n\tgpgsign = yes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f := &publicationFake{t: t, plan: j.Plan, directory: dir}
	pub := GitHubPublisher{Command: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "git" && args[len(args)-1] == "commit.gpgsign" {
			at := -1
			for i, arg := range args {
				if arg == "config" {
					at = i
					break
				}
			}
			if at >= 0 {
				commandArgs := append([]string{"config", "--file", config}, args[at+1:]...)
				return exec.CommandContext(ctx, "git", commandArgs...).Output()
			}
		}
		return f.command(ctx, name, args...)
	}}
	if _, err := pub.Publish(context.Background(), j.Plan, j.Workspace, dir, Publication{}, nil); err != nil {
		t.Fatal(err)
	}
	if f.signing != 1 || f.verifying != 1 || f.pushes != 1 {
		t.Fatalf("yes did not require signing and verification: %+v", f)
	}
}

func TestGitHubMissingChecksConfirmExactRevision(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			views := 0
			pub := GitHubPublisher{Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
				if args[1] == "checks" {
					return []byte(`[]`), errors.New("no checks yet")
				}
				views++
				head := testHead
				if changed && views == 2 {
					head = testTree
				}
				return []byte(`{"baseRefOid":"` + testBase + `","headRefOid":"` + head + `","state":"OPEN"}`), nil
			}}
			result, err := pub.Checks(context.Background(), Plan{Repository: "example/project"}, Publication{Number: 1, BaseSHA: testBase, HeadSHA: testHead})
			if views != 2 || changed && err == nil || !changed && (err != nil || result.Status != "missing") {
				t.Fatalf("result=%+v views=%d error=%v", result, views, err)
			}
		})
	}
}
