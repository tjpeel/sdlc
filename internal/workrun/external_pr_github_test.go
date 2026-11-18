package workrun

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestExternalPRGitHubMergePinsIncomingHead(t *testing.T) {
	b := externalBoundaryFixture()
	b.RepositoryID, b.IncomingHeadRepositoryID = 99, 99
	b.IncomingHeadRepository = b.Repository
	ref := func(name, sha string) map[string]any {
		return map[string]any{"ref": name, "sha": sha, "repo": map[string]any{"id": 99, "full_name": b.Repository}}
	}
	pull := func(number int, url string, base, head map[string]any) []byte {
		data, _ := json.Marshal(map[string]any{"number": number, "html_url": url, "state": "open", "base": base, "head": head})
		return data
	}
	var mergeArgs []string
	publisher := GitHubPublisher{Frozen: frozenTestIdentity(), Command: func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "gh" {
			return nil, fmt.Errorf("unexpected command")
		}
		if reflect.DeepEqual(args, []string{"api", "user"}) {
			return []byte(`{"id":123,"login":"example"}`), nil
		}
		if reflect.DeepEqual(args, []string{"api", "repos/example/project"}) {
			return []byte(`{"id":99,"full_name":"example/project"}`), nil
		}
		if reflect.DeepEqual(args, []string{"api", "repos/example/project/pulls/41"}) {
			return pull(41, b.ParentURL, ref(b.ParentBase, b.ParentBaseSHA), ref(b.ParentBranch, b.ParentHeadSHA)), nil
		}
		if reflect.DeepEqual(args, []string{"api", "repos/example/project/pulls/47"}) {
			return pull(47, b.IncomingURL, ref(b.ParentBranch, b.ParentHeadSHA), ref(b.IncomingHeadBranch, b.IncomingHeadSHA)), nil
		}
		if len(args) == 2 && args[1] == "repos/example/project/git/commits/"+b.ParentHeadSHA {
			return []byte(fmt.Sprintf(`{"sha":%q,"tree":{"sha":%q}}`, b.ParentHeadSHA, testTree)), nil
		}
		if len(args) > 2 && args[0] == "api" && args[1] == "--method" {
			mergeArgs = append([]string(nil), args...)
			return []byte(`{"merged":true}`), nil
		}
		return nil, fmt.Errorf("unexpected request %v", args)
	}}
	plan := Plan{Repository: b.Repository}
	o, err := publisher.ObserveExternalPR(context.Background(), plan, Publication{Number: b.ParentNumber}, b.IncomingNumber)
	if err != nil || o.ParentTree != testTree || ValidateExternalPRBeforeMerge(b, o) != nil {
		t.Fatalf("observation %+v: %v", o, err)
	}
	if err := publisher.MergeExternalPR(context.Background(), plan, b); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(mergeArgs, " "); !strings.Contains(got, "sha="+b.IncomingHeadSHA) || !strings.Contains(got, "merge_method=merge") {
		t.Fatal("merge request lacked pinned head or merge method:", got)
	}
}

func TestExternalPRMetadataUpdatesOnlyVerifiedMerge(t *testing.T) {
	b := externalBoundaryFixture()
	b.RepositoryID, b.IncomingHeadRepositoryID = 99, 99
	b.IncomingHeadRepository = b.Repository
	merge := strings.Repeat("d", 40)
	ref := func(name, sha string) map[string]any {
		return map[string]any{"ref": name, "sha": sha, "repo": map[string]any{"id": 99, "full_name": b.Repository}}
	}
	pull := func(number int, url string, base, head map[string]any, merged bool) []byte {
		state := "open"
		if merged {
			state = "closed"
		}
		data, _ := json.Marshal(map[string]any{"number": number, "html_url": url, "state": state, "merged": merged, "merge_commit_sha": merge, "base": base, "head": head})
		return data
	}
	var patch []string
	changed := false
	publisher := GitHubPublisher{Frozen: frozenTestIdentity(), Command: func(_ context.Context, _ string, args ...string) ([]byte, error) {
		if reflect.DeepEqual(args, []string{"api", "user"}) {
			return []byte(`{"id":123,"login":"example"}`), nil
		}
		if reflect.DeepEqual(args, []string{"api", "repos/example/project"}) {
			return []byte(`{"id":99,"full_name":"example/project"}`), nil
		}
		if reflect.DeepEqual(args, []string{"api", "repos/example/project/pulls/41"}) {
			head := merge
			if changed {
				head = testHead
			}
			return pull(41, b.ParentURL, ref(b.ParentBase, b.ParentBaseSHA), ref(b.ParentBranch, head), false), nil
		}
		if reflect.DeepEqual(args, []string{"api", "repos/example/project/pulls/47"}) {
			return pull(47, b.IncomingURL, ref(b.ParentBranch, b.ParentHeadSHA), ref(b.IncomingHeadBranch, b.IncomingHeadSHA), true), nil
		}
		if len(args) == 2 && strings.HasPrefix(args[1], "repos/example/project/git/commits/") {
			sha := strings.TrimPrefix(args[1], "repos/example/project/git/commits/")
			return []byte(fmt.Sprintf(`{"sha":%q,"tree":{"sha":%q}}`, sha, testTree)), nil
		}
		if reflect.DeepEqual(args, []string{"api", "repos/example/project/commits/" + merge}) {
			return []byte(fmt.Sprintf(`{"sha":%q,"parents":[{"sha":%q},{"sha":%q}]}`, merge, b.ParentHeadSHA, b.IncomingHeadSHA)), nil
		}
		if len(args) > 2 && args[0] == "api" && args[1] == "--method" && args[2] == "PATCH" {
			patch = append([]string(nil), args...)
			return []byte(`{}`), nil
		}
		return nil, fmt.Errorf("unexpected request %v", args)
	}}
	plan := Plan{Repository: b.Repository, Reference: "TASK-1", PRTitle: "Combined change", PRBody: "Details\n\nTests passed."}
	pub := Publication{Number: b.ParentNumber, URL: b.ParentURL, BaseSHA: b.ParentBaseSHA, HeadSHA: merge}
	if err := publisher.UpdateExternalPRMetadata(context.Background(), plan, pub, b); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(patch, "|"); !strings.Contains(got, "title=TASK-1: Combined change") || !strings.Contains(got, "body="+plan.PRBody) {
		t.Fatal("metadata changed:", got)
	}
	patch = nil
	changed = true
	if err := publisher.UpdateExternalPRMetadata(context.Background(), plan, pub, b); err == nil || patch != nil {
		t.Fatal("changed parent allowed a metadata update")
	}
}
