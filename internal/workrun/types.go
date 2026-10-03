// Package workrun coordinates one explicitly selected ticket. Provider clients
// do engineering work; the controller owns execution, evidence and publication.
package workrun

import (
	"context"
	"io"
	"time"
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

type Plan struct {
	Root        string     `json:"root"`
	Reference   string     `json:"reference"`
	Ticket      string     `json:"ticket"`
	StartingSHA string     `json:"starting_sha"`
	SourceSHA   string     `json:"source_sha"`
	Branch      string     `json:"branch"`
	Base        string     `json:"base"`
	BaseSHA     string     `json:"base_sha"`
	Repository  string     `json:"repository"`
	Inputs      []Input    `json:"inputs"`
	CheckInputs []Input    `json:"check_inputs"`
	Checks      [][]string `json:"checks"`
	DockerTests bool       `json:"docker_tests"`
	Roles       Roles      `json:"roles"`
	PRTitle     string     `json:"pr_title,omitempty"`
	PRBody      string     `json:"pr_body,omitempty"`
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
}

type SessionResult struct {
	Outcome       Outcome
	SessionID     string
	ReportedModel string
}

type Provider interface {
	Status(context.Context, string) (string, error)
	Execute(context.Context, Session, io.Writer, io.Writer) (SessionResult, error)
}

type CheckEvidence struct {
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
	Version            int           `json:"version"`
	ID                 string        `json:"id"`
	Plan               Plan          `json:"plan"`
	State              string        `json:"state"`
	Workspace          string        `json:"workspace"`
	SessionID          string        `json:"session_id"`
	ReportedModel      string        `json:"reported_model,omitempty"`
	Publication        Publication   `json:"publication"`
	ImageID            string        `json:"image_id"`
	Evidence           CheckEvidence `json:"evidence"`
	Outcome            Outcome       `json:"outcome"`
	Rounds             int           `json:"rounds"`
	Attempt            int           `json:"attempt"`
	CheckAttempt       int           `json:"check_attempt"`
	Instructions       string        `json:"instructions"`
	ResumeState        string        `json:"resume_state,omitempty"`
	PendingRole        string        `json:"pending_role,omitempty"`
	Feedback           string        `json:"feedback,omitempty"`
	MissingChecksSince time.Time     `json:"missing_checks_since,omitempty"`
	StopReason         string        `json:"stop_reason,omitempty"`
	CI                 CIResult      `json:"ci"`
	StartedAt          time.Time     `json:"started_at"`
	UpdatedAt          time.Time     `json:"updated_at"`
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
