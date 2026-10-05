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
	Base, SHA string
	Ancestors []string
}

type Result struct {
	RunID, Directory, State, Branch, Base, BaseSHA, HeadSHA, MergeSHA, URL, StopReason string
	PRNumber                                                                           int
}

type Observation struct{ State, Base, BaseSHA, HeadSHA, MergeSHA, CI, Details string }

type Driver interface {
	Base(context.Context, string) (Target, error)
	Execute(context.Context, Ticket, Target, *Result) (Result, error)
	Observe(context.Context, Result) (Observation, error)
	Reconcile(context.Context, Ticket, Target, Result) (Result, error)
}

type State struct {
	Version   int
	Plan      Plan
	Results   map[string]Result
	UpdatedAt time.Time
	Settings  json.RawMessage
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
