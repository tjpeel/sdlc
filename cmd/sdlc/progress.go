package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/tjpeel/sdlc/internal/dashboard"
	"github.com/tjpeel/sdlc/internal/runprogress"
	"github.com/tjpeel/sdlc/internal/runstatus"
	"github.com/tjpeel/sdlc/internal/runtimeimage"
)

const progressUsage = "Usage: sdlc progress --run RUN_ID | --launch-id UUID [--once | --follow] [--json] [--cursor CURSOR] [--scope project|installation] [--interval 500ms]\nFollow private run output without stopping the controller. Labels identify SDLC steps, checks and each provider's agent role. Terminal output follows by default; redirected output takes one snapshot. --json returns batches with an opaque cursor for incremental reads."

type progressOptions struct {
	run, launch, scope, cursor string
	once, follow, json         bool
	interval                   time.Duration
}

func parseProgressOptions(args []string) (progressOptions, error) {
	o := progressOptions{}
	f := flag.NewFlagSet("progress", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&o.run, "run", "", "run identity or unique prefix")
	f.StringVar(&o.launch, "launch-id", "", "terminal launch receipt")
	f.StringVar(&o.scope, "scope", "installation", "project or installation")
	f.StringVar(&o.cursor, "cursor", "", "opaque incremental cursor")
	f.BoolVar(&o.once, "once", false, "one batch")
	f.BoolVar(&o.follow, "follow", false, "follow until the controller stops")
	f.BoolVar(&o.json, "json", false, "structured batches")
	f.DurationVar(&o.interval, "interval", 500*time.Millisecond, "poll interval")
	if err := f.Parse(args); err != nil {
		return o, err
	}
	if f.NArg() != 0 || (o.run == "") == (o.launch == "") || o.once && o.follow || o.scope != "project" && o.scope != "installation" || o.interval < 100*time.Millisecond || o.interval > time.Minute {
		return o, fmt.Errorf("%s", progressUsage)
	}
	return o, nil
}

// The cursor contains offsets and status hashes only, never paths, prompts or
// log text. Binding it to the selector prevents mixing unrelated monitors.
type progressCursor struct {
	Version  int               `json:"version"`
	Selector string            `json:"selector"`
	Offsets  map[string]int64  `json:"offsets"`
	States   map[string]string `json:"states"`
}

var progressKey = regexp.MustCompile(`^[0-9a-f]{24}(/(events-[0-9]+\.jsonl|checks-[0-9]+\.log|diagnostics-[0-9]+\.log))?$`)

func progressDigest(value any) string {
	data, _ := json.Marshal(value)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func decodeProgressCursor(value, selector string) (progressCursor, error) {
	c := progressCursor{Version: 1, Selector: selector, Offsets: map[string]int64{}, States: map[string]string{}}
	if value == "" {
		return c, nil
	}
	if len(value) > 512*1024 {
		return c, fmt.Errorf("progress cursor is too large")
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return c, fmt.Errorf("invalid progress cursor")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(&c) != nil || d.Decode(new(any)) != io.EOF || c.Version != 1 || c.Selector != selector || len(c.Offsets) > 2048 || len(c.States) > 1025 || c.Offsets == nil || c.States == nil {
		return c, fmt.Errorf("progress cursor does not match this selection; start without --cursor")
	}
	for key, offset := range c.Offsets {
		if !progressKey.MatchString(key) || offset < 0 {
			return c, fmt.Errorf("invalid progress offset")
		}
	}
	for key, value := range c.States {
		if key != "launch" && !progressKey.MatchString(key) || len(value) != 64 {
			return c, fmt.Errorf("invalid progress state cursor")
		}
	}
	return c, nil
}

func progressCommand(ctx context.Context, args []string, output io.Writer) error {
	o, err := parseProgressOptions(args)
	if errors.Is(err, flag.ErrHelp) {
		_, err = fmt.Fprintln(output, progressUsage)
		return err
	}
	if err != nil {
		return err
	}
	runtime, err := runtimeimage.New(io.Discard, io.Discard)
	if err != nil {
		return err
	}
	root := ""
	if o.scope == "project" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		root, err = checkoutRoot(ctx, cwd)
		if err != nil {
			return fmt.Errorf("project progress requires a Git checkout; use --scope installation")
		}
	}
	follow := o.follow || dashboardTerminal(output) && !o.once && !o.json
	for {
		if ctx.Err() != nil {
			return nil
		}
		batch, err := progressSnapshot(ctx, runtime.Directory, root, o)
		if err != nil {
			return err
		}
		if o.json {
			err = json.NewEncoder(output).Encode(batch)
		} else {
			for _, event := range batch.Events {
				if _, err = fmt.Fprint(output, runprogress.Format(event)); err != nil {
					break
				}
			}
		}
		if err != nil || !follow || batch.Done {
			return err
		}
		o.cursor = batch.Cursor
		timer := time.NewTimer(o.interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func progressSnapshot(ctx context.Context, stateDir, root string, o progressOptions) (runprogress.Batch, error) {
	batch := runprogress.Batch{Events: []runprogress.Event{}, Done: true}
	used := 0
	appendEvent := func(event runprogress.Event) bool {
		data, _ := json.Marshal(event)
		if used+len(data)+1 > 128*1024 {
			batch.Done = false
			return false
		}
		used += len(data) + 1
		batch.Events = append(batch.Events, event)
		return true
	}
	if err := ctx.Err(); err != nil {
		return batch, err
	}
	views, err := runstatus.New(stateDir).List(time.Now().UTC())
	if err != nil {
		return batch, err
	}
	if root != "" {
		selected := views[:0]
		for _, v := range views {
			if sameProjectRoot(v.Root, root) {
				selected = append(selected, v)
			}
		}
		views = selected
	}
	selector := progressDigest([]string{stateDir, root, o.scope, o.run, o.launch})
	c, err := decodeProgressCursor(o.cursor, selector)
	if err != nil {
		return batch, err
	}
	selected := []runstatus.View{}
	if o.launch != "" {
		store, err := launchStore(stateDir)
		if err != nil {
			return batch, err
		}
		receipt, err := store.Status(o.launch)
		if err != nil {
			return batch, err
		}
		if root != "" && !sameProjectRoot(receipt.Root, root) {
			return batch, fmt.Errorf("launch belongs to another project; use --scope installation")
		}
		sig := progressDigest([]string{receipt.State, receipt.Error})
		if c.States["launch"] != sig {
			text := "Launch " + receipt.ID + ": " + receipt.State
			if receipt.Error != "" {
				text += "\n" + receipt.Error
			}
			if receipt.State == "unknown" || receipt.State == "unavailable" {
				text += "\nCheck the controller terminal and sdlc launch status --id " + receipt.ID
			}
			if appendEvent(progressStatus("", text)) {
				c.States["launch"] = sig
			}
		}
		batch.Done = receipt.State == "finished" || receipt.State == "failed"
		if len(receipt.RunIDs) > 1024 {
			return batch, fmt.Errorf("a progress feed supports at most 1024 launch runs; select a run with --run")
		}
		for _, id := range receipt.RunIDs {
			v, err := dashboard.Select(views, id)
			if err != nil {
				return batch, fmt.Errorf("launch run %s is unavailable: %w", id, err)
			}
			selected = append(selected, v)
		}
	} else {
		v, err := dashboard.Select(views, o.run)
		if err != nil {
			return batch, err
		}
		selected = append(selected, v)
	}
	for _, v := range selected {
		if v.Live || !v.Stopped && !v.Stale && v.Available {
			batch.Done = false
		}
		if v.Available && !v.Preparation {
			// Advance only after emitting a record. The aggregate bound applies
			// across every ticket in a feature, including escaped JSON text.
			for records := 0; ; records++ {
				events, next, err := runprogress.ReadBounded(v.Directory, c.Offsets[v.ID], 1)
				if errors.Is(err, os.ErrNotExist) {
					more, legacyErr := legacyProgress(v, &c, appendEvent)
					err = legacyErr
					if more {
						batch.Done = false
					}
					if err != nil {
						return batch, err
					}
					break
				}
				if err != nil {
					return batch, fmt.Errorf("run %s output unavailable: %w", v.ID, err)
				}
				if len(events) == 0 {
					break
				}
				if events[0].RunID != v.ID {
					return batch, fmt.Errorf("progress identity does not match selected run")
				}
				if records >= 512 || !appendEvent(events[0]) {
					batch.Done = false
					break
				}
				c.Offsets[v.ID] = next
			}
		}
		sig := progressDigest([]any{v.State, v.StopReason, v.Questions, v.Live, v.Stale, v.Stopped, v.Activity.WaitReason, v.Activity.WaitingProvider, v.Error, v.ActivityError})
		if c.States[v.ID] != sig {
			if appendEvent(progressStatus(v.ID, progressStateText(v))) {
				c.States[v.ID] = sig
			}
		}
	}
	data, err := json.Marshal(c)
	if err != nil {
		return batch, err
	}
	batch.Cursor = base64.RawURLEncoding.EncodeToString(data)
	if len(batch.Cursor) > 512*1024 {
		return batch, fmt.Errorf("progress cursor capacity exceeded; select a run with --run")
	}
	return batch, nil
}

func progressStatus(id, text string) runprogress.Event {
	if len(text) > 8*1024 {
		text = strings.ToValidUTF8(text[:8*1024], "") + " [truncated; inspect dashboard details]"
	}
	return runprogress.Event{Version: 1, Time: time.Now().UTC(), RunID: id, Source: "sdlc", Text: text}
}

func progressStateText(v runstatus.View) string {
	text := fmt.Sprintf("%s / %s: %s", v.Reference, filepath.Base(v.Ticket), v.State)
	if v.Live {
		text += " (controller active)"
	} else if v.Stale {
		text += " (controller heartbeat stale; inspect before resuming)"
	} else {
		text += " (controller stopped)"
	}
	if v.Activity.WaitReason != "" && v.Live {
		text += "\nWaiting: " + v.Activity.WaitingProvider + " / " + v.Activity.WaitReason
	}
	if v.StopReason != "" {
		text += "\n" + v.StopReason
	}
	if v.Error != "" || v.ActivityError != "" {
		text += "\n" + strings.TrimSpace(v.Error+" "+v.ActivityError)
	}
	for _, q := range v.Questions {
		text += "\nQuestion: " + q
	}
	if v.State == "waiting_for_human" {
		text += "\nAnswer: sdlc answer --run " + v.ID + " (shell: /answer " + v.ID + ")"
		if inputs := dashboard.InputAction(v); inputs != "" {
			text += "\nMissing file? " + inputs
		}
	} else if v.State == "ready" {
		text += "\nReady for human review: " + v.PR.URL
	} else if v.Stopped && !v.Preparation && v.Available {
		text += "\nInspect: sdlc dashboard --once --run " + v.ID + "; resume: sdlc resume --run " + v.ID
	}
	return text
}

func legacyProgress(v runstatus.View, cursor *progressCursor, appendEvent func(runprogress.Event) bool) (bool, error) {
	if v.Journal == nil {
		return false, nil
	}
	j := v.Journal
	name, source, provider, role := "", "agent", v.Provider, v.Role
	if (v.State == "checking" || (v.State == "blocked" || v.State == "failed") && j.ResumeState == "checking") && j.CheckAttempt > 0 {
		name, source, provider, role = fmt.Sprintf("checks-%d.log", j.CheckAttempt), "checks", "", ""
	} else if j.Attempt > 0 {
		name = fmt.Sprintf("events-%d.jsonl", j.Attempt)
	}
	if name == "" {
		return false, nil
	}
	key := v.ID + "/" + name
	// Only the selected legacy file is read. Discard old attempt offsets so
	// long features cannot accumulate an unbounded cursor across repairs.
	for old := range cursor.Offsets {
		if strings.HasPrefix(old, v.ID+"/") && old != key {
			delete(cursor.Offsets, old)
		}
	}
	for records := 0; records < 512; records++ {
		lines, next, err := runprogress.ReadLinesFinal(v.Directory, name, cursor.Offsets[key], 1, v.Stopped)
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if len(lines) == 0 {
			return false, nil
		}
		texts := []string{lines[0]}
		if source == "agent" {
			// Older journals describe the current stage, which can differ from
			// the producer of the last log (for example after review finishes).
			var native struct {
				Type string `json:"type"`
			}
			if json.Unmarshal([]byte(lines[0]), &native) == nil {
				switch {
				case strings.HasPrefix(native.Type, "item."), strings.HasPrefix(native.Type, "thread."), strings.HasPrefix(native.Type, "turn."):
					provider = "codex"
				case native.Type == "assistant", native.Type == "user", native.Type == "system", native.Type == "result":
					provider = "claude"
				}
				role = "implementation"
				if provider == j.Plan.Roles.Review.Provider {
					role = "review"
				}
			}
			texts = runprogress.Summarize(provider, []byte(lines[0]))
		}
		// A native record can contain several blocks. Emit it as one event so
		// a full batch cannot advance past only part of the record.
		text := strings.Join(texts, "\n")
		if len(text) > 8*1024 {
			text = strings.ToValidUTF8(text[:8*1024], "") + " [truncated; see private raw log]"
		}
		if len(texts) > 0 && !appendEvent(runprogress.Event{Version: 1, Time: time.Now().UTC(), RunID: v.ID, Source: source, Provider: provider, Role: role, Stage: v.State, Attempt: j.Attempt, Text: text}) {
			return true, nil
		}
		cursor.Offsets[key] = next
	}
	return true, nil
}
