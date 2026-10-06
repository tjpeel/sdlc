// Package workrun coordinates one explicitly selected ticket. Provider clients
// do engineering work; the controller owns execution, evidence and publication.
package workrun

import (
	"context"
	"io"
	"time"

	"github.com/tjpeel/sdlc/internal/headroom"
	"github.com/tjpeel/sdlc/internal/runtimepins"
)

type Model struct {
	Provider string `json:"provider"`
	Name     string `json:"model"`
	Effort   string `json:"effort"`
}

type Roles struct {
	Implementation Model `json:"implementation"`
	Review         Model `json:"review"`
}

type Input struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type PublicationIdentity struct {
	GitHubProfile  string `json:"github_profile"`
	RepositoryID   int64  `json:"repository_id"`
	RepositoryName string `json:"repository_name"`
	GitHubVolume   string `json:"github_volume"`
	ProfileID      string `json:"profile_id"`
	GitHubID       int64  `json:"github_id"`
	GitHubLogin    string `json:"github_login"`
	GitName        string `json:"git_name"`
	GitEmail       string `json:"git_email"`
	SSHPublicKey   string `json:"ssh_public_key"`
	SSHFingerprint string `json:"ssh_fingerprint"`
}

type Plan struct {
	Headroom            headroom.Config      `json:"headroom,omitzero"`
	GitHubProfile       string               `json:"github_profile,omitempty"`
	SigningProfile      string               `json:"signing_profile,omitempty"`
	PublicationIdentity *PublicationIdentity `json:"publication_identity,omitempty"`
	Root                string               `json:"root"`
	Reference           string               `json:"reference"`
	Ticket              string               `json:"ticket"`
	StartingSHA         string               `json:"starting_sha"`
	SourceSHA           string               `json:"source_sha"`
	Branch              string               `json:"branch"`
	Base                string               `json:"base"`
	BaseSHA             string               `json:"base_sha"`
	Repository          string               `json:"repository"`
	Inputs              []Input              `json:"inputs"`
	CheckInputs         []Input              `json:"check_inputs"`
	Checks              [][]string           `json:"checks"`
	DockerTests         bool                 `json:"docker_tests"`
	SigningImage        string               `json:"signing_image,omitempty"`
	DaemonImage         string               `json:"daemon_image,omitempty"`
	Roles               Roles                `json:"roles"`
	PRTitle             string               `json:"pr_title,omitempty"`
	PRBody              string               `json:"pr_body,omitempty"`
	Restack             *RestackBoundary     `json:"restack,omitempty"`
}

// RestackBoundary is the exact remote PR boundary which a restack may move.
// It prevents a resumed controller from retargeting an unrelated PR.
type RestackBoundary struct {
	Base    string `json:"base"`
	BaseSHA string `json:"base_sha"`
	HeadSHA string `json:"head_sha"`
	Number  int    `json:"number"`
}

type BranchSnapshot struct {
	Branch string `json:"branch"`
	SHA    string `json:"sha"`
	Bundle string `json:"bundle"`
}

type RemotePR struct {
	State    string `json:"state"`
	Base     string `json:"base"`
	BaseSHA  string `json:"base_sha"`
	HeadSHA  string `json:"head_sha"`
	MergeSHA string `json:"merge_sha"`
}

type RebaseResult struct {
	Conflict bool     `json:"conflict"`
	Paths    []string `json:"paths"`
}

// Reconciliation is checkpointed before the workspace is changed. Its source
// and target revisions form the idempotence key used by DockerRepository.
type Reconciliation struct {
	OldSource string         `json:"old_source"`
	Snapshot  BranchSnapshot `json:"snapshot"`
	State     string         `json:"state"`
	Result    RebaseResult   `json:"result"`
}

// ValidateSidecarImages accepts absent pins for journals written before runtime
// sidecars were selected per installation. Callers retain the historical defaults.
func (plan Plan) ValidateSidecarImages() error {
	if err := plan.Headroom.Validate(); err != nil {
		return err
	}
	if plan.SigningImage != "" {
		if err := runtimepins.ValidateSigningImage(plan.SigningImage); err != nil {
			return err
		}
	}
	if plan.DaemonImage != "" {
		if err := runtimepins.ValidateDaemonImage(plan.DaemonImage); err != nil {
			return err
		}
	}
	return nil
}

type Finding struct {
	Priority       string `json:"priority"`
	Path           string `json:"path"`
	Line           int    `json:"line"`
	Scenario       string `json:"scenario"`
	Recommendation string `json:"recommendation"`
}

type Outcome struct {
	Status      string    `json:"status"`
	Summary     string    `json:"summary"`
	Questions   []string  `json:"questions"`
	Findings    []Finding `json:"findings"`
	LocalReview bool      `json:"local_review"`
	Limitations []string  `json:"limitations"`
	PRTitle     string    `json:"pr_title"`
	PRBody      string    `json:"pr_body"`
}

type Session struct {
	Model        Model
	Role         string
	Workspace    string
	Directory    string
	Prompt       string
	Schema       string
	ResumeID     string
	Instructions string
	Headroom     headroom.Config
}

type SessionResult struct {
	Outcome       Outcome
	SessionID     string
	ReportedModel string
	HeadroomStats *headroom.Stats
}

type Provider interface {
	Status(context.Context, string) (string, error)
	Execute(context.Context, Session, io.Writer, io.Writer) (SessionResult, error)
}

type CheckEvidence struct {
	Head     string     `json:"head,omitempty"`
	Tree     string     `json:"tree"`
	Passed   bool       `json:"passed"`
	Commands [][]string `json:"commands"`
	Log      string     `json:"log"`
}

type Checker interface {
	Check(context.Context, string, [][]string, io.Writer) error
}

type Publication struct {
	URL     string `json:"url"`
	Number  int    `json:"number"`
	BaseSHA string `json:"base_sha"`
	HeadSHA string `json:"head_sha"`
}

type CIResult struct {
	Status  string `json:"status"`
	Details string `json:"details"`
}

type Publisher interface {
	Publish(context.Context, Plan, string, string, Publication, io.Writer) (Publication, error)
	Checks(context.Context, Plan, Publication) (CIResult, error)
}

type Journal struct {
	Timings            Timings           `json:"timings"`
	Version            int               `json:"version"`
	ID                 string            `json:"id"`
	Plan               Plan              `json:"plan"`
	State              string            `json:"state"`
	Workspace          string            `json:"workspace"`
	SessionID          string            `json:"session_id"`
	ReportedModel      string            `json:"reported_model,omitempty"`
	Publication        Publication       `json:"publication"`
	ImageID            string            `json:"image_id"`
	Evidence           CheckEvidence     `json:"evidence"`
	Outcome            Outcome           `json:"outcome"`
	Rounds             int               `json:"rounds"`
	Attempt            int               `json:"attempt"`
	CheckAttempt       int               `json:"check_attempt"`
	Instructions       string            `json:"instructions"`
	ResumeState        string            `json:"resume_state,omitempty"`
	PendingRole        string            `json:"pending_role,omitempty"`
	Feedback           string            `json:"feedback,omitempty"`
	MissingChecksSince time.Time         `json:"missing_checks_since,omitempty"`
	StopReason         string            `json:"stop_reason,omitempty"`
	CI                 CIResult          `json:"ci"`
	Reconciliation     *Reconciliation   `json:"reconciliation,omitempty"`
	RestackAudit       []RestackBoundary `json:"restack_audit,omitempty"`
	StartedAt          time.Time         `json:"started_at"`
	UpdatedAt          time.Time         `json:"updated_at"`
}

// Timings are observed controller intervals. They exclude time between
// controller invocations; they are not active model-compute durations.
type Timings struct {
	Version      int   `json:"version"`
	ControllerMS int64 `json:"controller_ms"`
	ChecksMS     int64 `json:"checks_ms"`
	CIWaitMS     int64 `json:"ci_wait_ms"`
}

func (t Timings) Recorded() *Timings {
	if t.Version != 1 {
		return nil
	}
	return &t
}

type Revision struct {
	Head  string `json:"head"`
	Tree  string `json:"tree"`
	Clean bool   `json:"clean"`
}

type Repository interface {
	Inspect(context.Context, string) (Revision, error)
	Bundle(context.Context, string, string) error
}
