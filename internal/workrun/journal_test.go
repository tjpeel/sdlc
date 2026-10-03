package workrun

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjpeel/sdlc/internal/filelock"
)

func TestJournalRoundTripRetainsRecoveryEvidence(t *testing.T) {
	dir, j := testRun(t)
	j.State = "blocked"
	j.ResumeState = "publishing"
	j.SessionID = testNative
	j.Evidence = CheckEvidence{Tree: testTree, Passed: true, Log: "checks-1.log"}
	j.Publication = Publication{URL: "https://github.com/example/project/pull/1", Number: 1, HeadSHA: testHead, BaseSHA: testBase}
	j.Rounds = 2
	j.Attempt = 4
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	got, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.ResumeState != j.ResumeState || got.SessionID != j.SessionID || got.Evidence.Tree != testTree || got.Publication != j.Publication || got.Rounds != 2 || got.Attempt != 4 || got.UpdatedAt.IsZero() {
		t.Fatalf("lost recovery state: %+v", got)
	}
	info, err := os.Stat(filepath.Join(dir, "journal.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("unsafe journal permissions: %v %v", info, err)
	}
}
func TestJournalRejectsOutsideWorkspaceAndUnknownFields(t *testing.T) {
	for _, kind := range []string{"workspace", "unknown", "version", "source revision", "identity", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			dir, j := testRun(t)
			switch kind {
			case "workspace":
				j.Workspace = filepath.Dir(dir)
			case "version":
				j.Version = 2
			case "source revision":
				j.Plan.SourceSHA = "short"
			case "identity":
				j.ID = "other"
			}
			data, _ := json.Marshal(j)
			if kind == "unknown" {
				data = append(data[:len(data)-1], []byte(`,"extra":true}`)...)
			}
			path := filepath.Join(dir, "journal.json")
			if kind == "symlink" {
				target := filepath.Join(dir, "other.json")
				os.WriteFile(target, data, 0600)
				os.Symlink(target, path)
			} else {
				os.WriteFile(path, data, 0600)
			}
			if _, err := Load(dir); err == nil {
				t.Fatal("unsafe journal accepted")
			}
		})
	}
}
func TestRunnerLockPreventsConcurrentExecution(t *testing.T) {
	dir, j := testRun(t)
	lock, err := filelock.Acquire(filepath.Join(dir, "run.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	p := &fakeProvider{}
	pub := &fakePublisher{}
	r := fakeRunner(p, &fakeChecker{}, pub, &fakeRepository{})
	if err := r.Run(context.Background(), dir, j, ""); err == nil || len(p.calls) != 0 || pub.calls != 0 {
		t.Fatal("second controller obtained active run")
	}
}

func TestSaveCannotReplaceSymlinkedJournal(t *testing.T) {
	dir, j := testRun(t)
	target := filepath.Join(dir, "untouched")
	if err := os.WriteFile(target, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(dir, "journal.json")); err != nil {
		t.Fatal(err)
	}
	if err := Save(dir, j); err == nil {
		t.Fatal("journal symlink replaced")
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "original" {
		t.Fatal("symlink target modified")
	}
}

func TestOversizedJournalSavePreservesPriorCheckpoint(t *testing.T) {
	dir, j := testRun(t)
	j.State = "blocked"
	j.ResumeState = "publishing"
	j.SessionID = testNative
	j.Outcome = testOutcome("implemented")
	if err := Save(dir, j); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	j.Outcome.Summary = strings.Repeat("x", 1024*1024)
	if err := Save(dir, j); err == nil {
		t.Fatal("oversized journal replaced resumable checkpoint")
	}
	after, err := os.ReadFile(filepath.Join(dir, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("oversized save changed prior checkpoint")
	}
	loaded, err := Load(dir)
	if err != nil || loaded.State != "blocked" || loaded.ResumeState != "publishing" || loaded.SessionID != testNative || loaded.Outcome.Summary != testOutcome("implemented").Summary {
		t.Fatalf("prior checkpoint cannot resume: %+v %v", loaded, err)
	}
}
