// Package workseries plans and checkpoints an offline series of local work tickets.
package workseries

import (
	"context"
	"encoding/json"
	"io"
	"time"
)

type Plan struct {
	Version       int
	Root          string
	Reference     string
	Base          string
	Tickets       []Ticket
	DefinitionSHA string
}

type Ticket struct {
	File      string
	DependsOn []string
	Priority  int
	Touches   []string
}

type Target struct {
	Base, SHA    string
	Ancestors    []string
	Dependencies []Dependency
	Context      json.RawMessage
}

// Dependency is an exact ordered publication boundary, including squash merges.
type Dependency struct {
	Ticket, RunID, Branch, Base, BaseSHA, HeadSHA, State, MergeSHA string
	PRNumber                                                       int
}

// CombinedDriver prepares and checks a private aggregate before exposing a base.
type CombinedDriver interface {
	Combine(context.Context, Ticket, Target, []Dependency) (Target, error)
	Invalidate(context.Context, Ticket, Result) error
}

type Result struct {
	DependencyInvalid                                                                  bool `json:"DependencyInvalid,omitempty"`
	RunID, Directory, State, Branch, Base, BaseSHA, HeadSHA, MergeSHA, URL, StopReason string
	PRNumber                                                                           int
	Dependencies                                                                       []Dependency `json:"Dependencies,omitempty"`
}

type Observation struct{ State, Base, BaseSHA, HeadSHA, MergeSHA, CI, Details string }

type Driver interface {
	Base(context.Context, string) (Target, error)
	Execute(context.Context, Ticket, Target, *Result) (Result, error)
	Observe(context.Context, Result) (Observation, error)
	Reconcile(context.Context, Ticket, Target, Result) (Result, error)
}

type State struct {
	DependencyAmendments []DependencyAmendmentAudit `json:"dependency_amendments,omitempty"`
	Version              int
	Plan                 Plan
	Results              map[string]Result
	UpdatedAt            time.Time
	Settings             json.RawMessage
}

type Runner struct {
	Driver       Driver
	Output       io.Writer
	Parallel     int
	Watch        bool
	PollInterval time.Duration
	OnChange     func(State) error
	Initialize   func(context.Context, *State) error
}
