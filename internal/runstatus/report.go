package runstatus

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/tjpeel/sdlc/internal/runusage"
	"github.com/tjpeel/sdlc/internal/workrun"
)

// Report is a portable private checkpoint summary, not a resumable backup.
// Explicit projection avoids copying authentication metadata, prompts, session
// identifiers, raw logs, private input bytes or the captured source workspace.
type Report struct {
	Timings       *workrun.Timings    `json:"timings,omitempty"`
	Metrics       *runusage.Summary   `json:"metrics,omitempty"`
	MetricsError  string              `json:"metrics_error,omitempty"`
	Version       int                 `json:"version"`
	ExportedAt    time.Time           `json:"exported_at"`
	ID            string              `json:"id"`
	State         string              `json:"state"`
	StartedAt     time.Time           `json:"started_at"`
	UpdatedAt     time.Time           `json:"updated_at"`
	Repository    string              `json:"repository"`
	Reference     string              `json:"reference"`
	Ticket        string              `json:"ticket"`
	Branch        string              `json:"branch"`
	Base          string              `json:"base"`
	StartingSHA   string              `json:"starting_sha"`
	SourceSHA     string              `json:"source_sha"`
	BaseSHA       string              `json:"base_sha"`
	ImageID       string              `json:"image_id"`
	Roles         workrun.Roles       `json:"roles"`
	ReportedModel string              `json:"reported_model,omitempty"`
	Inputs        []workrun.Input     `json:"inputs"`
	CheckInputs   []workrun.Input     `json:"check_input_hashes"`
	Checks        [][]string          `json:"checks"`
	Evidence      ReportEvidence      `json:"evidence"`
	Publication   workrun.Publication `json:"publication"`
	CI            ReportCI            `json:"ci"`
	Summary       string              `json:"summary"`
	LocalReview   bool                `json:"local_review"`
	Findings      []workrun.Finding   `json:"findings"`
	Limitations   []string            `json:"limitations"`
	Questions     []string            `json:"questions"`
	StopReason    string              `json:"stop_reason,omitempty"`
	Rounds        int                 `json:"repair_rounds"`
	Attempts      int                 `json:"provider_attempts"`
	CheckAttempts int                 `json:"check_attempts"`
	Retention     string              `json:"retention"`
}

type ReportEvidence struct {
	Head     string     `json:"head,omitempty"`
	Tree     string     `json:"tree"`
	Passed   bool       `json:"passed"`
	Commands [][]string `json:"commands"`
}

type ReportCI struct {
	Status string `json:"status"`
}

func reportSnapshot(v View) (Report, error) {
	if !v.Available || v.Journal == nil || !identifier.MatchString(v.ID) || v.Journal.ID != v.ID {
		return Report{}, fmt.Errorf("a valid available run checkpoint is required for export")
	}
	j := v.Journal
	questions := []string(nil)
	if j.State == "waiting_for_human" {
		questions = j.Outcome.Questions
	}
	return Report{
		Timings: j.Timings.Recorded(),
		Metrics: v.Metrics, MetricsError: v.MetricsError,
		Version: 1, ExportedAt: time.Now().UTC(), ID: j.ID, State: j.State,
		StartedAt: j.StartedAt, UpdatedAt: j.UpdatedAt,
		Repository: j.Plan.Repository, Reference: j.Plan.Reference, Ticket: j.Plan.Ticket,
		Branch: j.Plan.Branch, Base: j.Plan.Base, StartingSHA: j.Plan.StartingSHA, SourceSHA: j.Plan.SourceSHA, BaseSHA: j.Plan.BaseSHA,
		ImageID: j.ImageID, Roles: j.Plan.Roles, ReportedModel: j.ReportedModel,
		Inputs: j.Plan.Inputs, CheckInputs: j.Plan.CheckInputs, Checks: j.Plan.Checks,
		Evidence:    ReportEvidence{j.Evidence.Head, j.Evidence.Tree, j.Evidence.Passed, j.Evidence.Commands},
		Publication: j.Publication, CI: ReportCI{Status: j.CI.Status}, Summary: j.Outcome.Summary, LocalReview: j.Outcome.LocalReview,
		Findings: j.Outcome.Findings, Limitations: j.Outcome.Limitations, Questions: questions, StopReason: j.StopReason,
		Rounds: j.Rounds, Attempts: j.Attempt, CheckAttempts: j.CheckAttempt,
		Retention: "Checkpoint summary only. Raw logs, native session data, private input contents and workspace are excluded. Keep this report private; summaries and check commands can contain sensitive material.",
	}, nil
}

// ExportReport writes a new 0600 file in an existing owned 0700 directory.
// An anchored directory and exclusive creation prevent symlink traversal and
// overwriting prior reports. No provider, credential store or Docker is contacted.
func ExportReport(v View, destination string) (string, error) {
	report, err := reportSnapshot(v)
	if err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil || len(data)+1 > 1024*1024 {
		return "", fmt.Errorf("run report exceeds the private export limit")
	}
	root, err := historyDirectory(destination)
	if err != nil {
		return "", fmt.Errorf("export destination must be an existing absolute owned 0700 directory without symlinks")
	}
	defer root.Close()
	for parent := destination; ; parent = filepath.Dir(parent) {
		if _, err := os.Lstat(filepath.Join(parent, ".git")); err == nil || !os.IsNotExist(err) {
			return "", fmt.Errorf("keep private run reports outside Git checkouts")
		}
		if filepath.Dir(parent) == parent {
			break
		}
	}
	name := v.ID + ".report.json"
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", fmt.Errorf("cannot create private run report; check destination or choose a new directory for a repeated export")
	}
	info, statErr := file.Stat()
	complete := false
	defer func() {
		file.Close()
		if !complete && statErr == nil {
			if current, err := root.Lstat(name); err == nil && os.SameFile(current, info) {
				_ = root.Remove(name)
			}
		}
	}()
	if statErr != nil || info.Mode().Perm() != 0600 || !historyOwned(info, true) {
		return "", fmt.Errorf("cannot establish private run report permissions")
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return "", fmt.Errorf("cannot write private run report")
	}
	if err := file.Sync(); err != nil {
		return "", fmt.Errorf("cannot save private run report")
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("cannot close private run report")
	}
	if err := historySameDirectory(root, destination); err != nil {
		return "", err
	}
	current, err := root.Lstat(name)
	if err != nil || !os.SameFile(info, current) || current.Mode().Perm() != 0600 || !historyOwned(current, true) {
		return "", fmt.Errorf("private run report changed during export")
	}
	complete = true
	return filepath.Join(destination, name), nil
}
