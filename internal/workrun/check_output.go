package workrun

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"regexp"
	"sort"
	"sync"
	"unicode/utf8"
)

const commandOutputBudget = 64 * 1024

// CommandOutput is process data, separate from the controller's result identity.
// Combined stdout/stderr has no guaranteed ordering across the two streams.
type CommandOutput struct {
	Encoding       string `json:"encoding,omitempty"`
	ExitCode       int    `json:"exit_code"`
	Completed      bool   `json:"completed"`
	Command        int    `json:"command"`
	Phase          string `json:"phase"`
	CombinedOutput string `json:"combined_output"`
	TotalBytes     int64  `json:"total_bytes"`
	OmittedBytes   int64  `json:"omitted_bytes"`
	Truncated      bool   `json:"truncated"`
	HeadBytes      int    `json:"head_bytes"`
	TailBytes      int    `json:"tail_bytes"`
	FailureBytes   int    `json:"failure_bytes,omitempty"`
	FailureOffset  int64  `json:"failure_offset,omitempty"`
}

type commandOutputCollector struct {
	io.Writer
	captures  []*boundedCommandOutput
	remaining int
}

func newCommandOutputCollector(output io.Writer) *commandOutputCollector {
	return &commandOutputCollector{Writer: output, remaining: commandOutputBudget}
}
func (c *commandOutputCollector) commandOutput(command int, baseline bool) io.Writer {
	limit := c.remaining
	if limit > 16*1024 {
		limit = 16 * 1024
	}
	c.remaining -= limit
	phase := "candidate"
	if baseline {
		phase = "baseline"
	}
	b := &boundedCommandOutput{limit: limit, result: CommandOutput{Command: command, Phase: phase}}
	b.releaseUnused = func(bytes int) { c.remaining += bytes }
	c.captures = append(c.captures, b)
	return b
}
func (c *commandOutputCollector) results() []CommandOutput {
	result := make([]CommandOutput, 0, len(c.captures))
	for _, b := range c.captures {
		result = append(result, b.snapshot())
	}
	return result
}

type boundedCommandOutput struct {
	sync.Mutex
	limit                     int
	head, tail                []byte
	line, failure             []byte
	lineOffset, failureOffset int64
	result                    CommandOutput
	releaseUnused             func(int)
}

func (b *boundedCommandOutput) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	n := len(p)
	b.captureFailure(p, b.result.TotalBytes)
	b.result.TotalBytes += int64(n)
	headLimit := (b.limit + 1) / 2
	take := headLimit - len(b.head)
	if take > len(p) {
		take = len(p)
	}
	b.head = append(b.head, p[:take]...)
	p = p[take:]
	tailLimit := b.limit - headLimit
	if tailLimit > 0 {
		if len(p) >= tailLimit {
			b.tail = append(b.tail[:0], p[len(p)-tailLimit:]...)
		} else {
			if excess := len(b.tail) + len(p) - tailLimit; excess > 0 {
				copy(b.tail, b.tail[excess:])
				b.tail = b.tail[:len(b.tail)-excess]
			}
			b.tail = append(b.tail, p...)
		}
	}
	return n, nil
}

// Markers select literal process context; only a reported nonzero exit activates it.
var commandFailureMarker = regexp.MustCompile(`(?i)(\[FAIL\]|\berror(?:\s+[A-Z][A-Z0-9]*[0-9]|\[[A-Z][0-9]+\])?\s*:|^\s*(?:---\s+FAIL:|FAIL:|Failed\s+[A-Z_]|Traceback \(most recent call last\):))`)

// Reserve most of a small window for its actual marker and following context.
func failureContextStart(match []int, limit int) int {
	prefix := limit / 4
	if prefix > 256 {
		prefix = 256
	}
	if available := limit - (match[1] - match[0]); prefix > available {
		prefix = available
	}
	if prefix < 0 {
		prefix = 0
	}
	start := match[0] - prefix
	if start < 0 {
		return 0
	}
	return start
}

func (b *boundedCommandOutput) captureFailure(data []byte, offset int64) {
	maximum := b.limit
	if maximum > 8*1024 {
		maximum = 8 * 1024
	}
	if maximum <= 0 {
		return
	}
	for len(data) > 0 && len(b.failure) == 0 {
		size := len(data)
		if size > 1024 {
			size = 1024
		}
		if newline := bytes.IndexByte(data[:size], '\n'); newline >= 0 {
			size = newline + 1
		}
		if len(b.line) == 0 {
			b.lineOffset = offset
		}
		b.line = append(b.line, data[:size]...)
		offset += int64(size)
		data = data[size:]
		// Match across the old/new scan boundary before discarding context.
		// Keep a short actual prefix so a long line cannot bury its marker.
		if match := commandFailureMarker.FindIndex(b.line); match != nil {
			start := failureContextStart(match, maximum)
			b.failureOffset = b.lineOffset + int64(start)
			context := b.line[start:]
			if len(context) > maximum {
				context = context[:maximum]
			}
			b.failure = append(b.failure, context...)
		} else if len(b.line) > 1024 {
			discard := len(b.line) - 1024
			b.line = append(b.line[:0], b.line[discard:]...)
			b.lineOffset += int64(discard)
		}
		if len(b.line) > 0 && b.line[len(b.line)-1] == '\n' {
			b.line = b.line[:0]
		}
	}
	if len(b.failure) > 0 && len(b.failure) < maximum {
		remaining := maximum - len(b.failure)
		if remaining > len(data) {
			remaining = len(data)
		}
		b.failure = append(b.failure, data[:remaining]...)
	}
}
func (b *boundedCommandOutput) completed(exit int) {
	b.Lock()
	defer b.Unlock()
	first := !b.result.Completed
	b.result.Completed = true
	b.result.ExitCode = exit
	if first && b.releaseUnused != nil {
		b.releaseUnused(b.limit - outputRetained(b.snapshotLocked()))
	}
}
func (b *boundedCommandOutput) snapshot() CommandOutput {
	b.Lock()
	defer b.Unlock()
	return b.snapshotLocked()
}
func (b *boundedCommandOutput) snapshotLocked() CommandOutput {
	r := b.result
	r.HeadBytes = len(b.head)
	r.TailBytes = len(b.tail)
	data := append([]byte(nil), b.head...)
	if r.Completed && r.ExitCode != 0 && r.TotalBytes <= int64(b.limit) && len(b.failure) > 0 {
		data = append(data, b.tail...)
		r.HeadBytes = int(b.failureOffset)
		r.FailureOffset = b.failureOffset
		r.FailureBytes = len(b.failure)
		r.TailBytes = len(data) - r.HeadBytes - r.FailureBytes
		setCommandOutputData(&r, data)
		r.OmittedBytes = 0
		r.Truncated = false
		return r
	}
	if r.Completed && r.ExitCode != 0 && r.TotalBytes > int64(b.limit) && len(b.failure) > 0 {
		r.FailureOffset = b.failureOffset
		r.FailureBytes = len(b.failure)
		data = append(data, b.failure...)
	}
	data = append(data, b.tail...)
	setCommandOutputData(&r, data)
	r.OmittedBytes = r.TotalBytes - int64(len(data))
	r.Truncated = r.OmittedBytes > 0
	return trimCommandOutput(r, b.limit)
}
func outputRetained(r CommandOutput) int { return r.HeadBytes + r.FailureBytes + r.TailBytes }
func trimCommandOutput(r CommandOutput, limit int) CommandOutput {
	data, _ := commandOutputBytes(r)
	if len(data) <= limit {
		return r
	}
	failure := r.FailureBytes
	if failure > limit {
		failure = limit
	}
	failureStart := 0
	if failure > 0 && failure < r.FailureBytes {
		context := data[r.HeadBytes : r.HeadBytes+r.FailureBytes]
		if match := commandFailureMarker.FindIndex(context); match != nil {
			failureStart = failureContextStart(match, failure)
		}
		if failureStart+failure > r.FailureBytes {
			failureStart = r.FailureBytes - failure
		}
		r.FailureOffset += int64(failureStart)
	}
	available := limit - failure
	head := (available + 1) / 2
	tail := available - head
	headMaximum, tailMaximum := r.HeadBytes, r.TailBytes
	if failure > 0 {
		if int64(headMaximum) > r.FailureOffset {
			headMaximum = int(r.FailureOffset)
		}
		after := r.TotalBytes - r.FailureOffset - int64(failure)
		if int64(tailMaximum) > after {
			tailMaximum = int(after)
		}
	}
	if head > headMaximum {
		head = headMaximum
		tail = available - head
	}
	if tail > tailMaximum {
		tail = tailMaximum
		head = available - tail
		if head > headMaximum {
			head = headMaximum
		}
	}
	selected := append([]byte(nil), data[:head]...)
	if failure > 0 {
		selected = append(selected, data[r.HeadBytes+failureStart:r.HeadBytes+failureStart+failure]...)
	} else {
		r.FailureOffset = 0
	}
	selected = append(selected, data[len(data)-tail:]...)
	setCommandOutputData(&r, selected)
	r.HeadBytes, r.FailureBytes, r.TailBytes = head, failure, tail
	r.OmittedBytes = r.TotalBytes - int64(len(selected))
	r.Truncated = r.OmittedBytes > 0
	return r
}
func boundJournalOutput(j *Journal) {
	boundOutputs(j, 16*1024)
}
func boundOutputs(j *Journal, budget int) {
	var outputs []*CommandOutput
	retained := 0
	for i := range j.Evidence.Output {
		outputs = append(outputs, &j.Evidence.Output[i])
	}
	for i := range j.FormatterPreparation {
		for k := range j.FormatterPreparation[i].Output {
			outputs = append(outputs, &j.FormatterPreparation[i].Output[k])
		}
	}
	for i := range j.Verification {
		for k := range j.Verification[i].Output {
			outputs = append(outputs, &j.Verification[i].Output[k])
		}
	}
	for i := range j.Diagnostics {
		for k := range j.Diagnostics[i].Output {
			outputs = append(outputs, &j.Diagnostics[i].Output[k])
		}
	}
	for _, output := range outputs {
		retained += outputRetained(*output)
	}
	if retained <= budget {
		return
	}
	// Preserve short results and allocate their unused shares to longer output.
	sort.Slice(outputs, func(i, k int) bool {
		return outputRetained(*outputs[i]) < outputRetained(*outputs[k])
	})
	for i, output := range outputs {
		*output = trimCommandOutput(*output, budget/(len(outputs)-i))
		budget -= outputRetained(*output)
	}
}
func effectiveChecks(j *Journal) [][]string {
	if j.Plan.CheckSelection == "runtime" {
		return j.EffectiveChecks
	}
	return j.Plan.Checks
}

func invalidateCommandEvidence(j *Journal) {
	clearDiagnosticEvidence(j)
	clearFormatterPreparation(j)
	j.Evidence.Passed = false
	j.Evidence.Key = ""
	j.Evidence.Output = nil
	j.Evidence.CompletedFailureKey = ""
	j.Evidence.CompletedFailureFeedback = ""
	j.ConfiguredFeedbackKey = ""
	j.Feedback = ""
	for i := range j.Verification {
		j.Verification[i].Key = ""
		j.Verification[i].Output = nil
		discardSourcePatchFeedback(j, &j.Verification[i])
	}
}

func commandOutputBytes(r CommandOutput) ([]byte, error) {
	if r.Encoding == "base64" {
		return base64.StdEncoding.DecodeString(r.CombinedOutput)
	}
	if r.Encoding != "" {
		return nil, fmt.Errorf("unsupported process output encoding")
	}
	return []byte(r.CombinedOutput), nil
}
func setCommandOutputData(r *CommandOutput, data []byte) {
	r.Encoding = ""
	if utf8.Valid(data) {
		r.CombinedOutput = string(data)
	} else {
		r.Encoding = "base64"
		r.CombinedOutput = base64.StdEncoding.EncodeToString(data)
	}
}
func validateCommandEvidence(j *Journal) error {
	retained := 0
	validate := func(results []CommandOutput) error {
		if len(results) > 128 {
			return fmt.Errorf("too many command output results")
		}
		for _, r := range results {
			data, err := commandOutputBytes(r)
			if err != nil || !r.Completed || r.Command < 1 || r.Command > 64 || (r.Phase != "candidate" && r.Phase != "baseline") || r.ExitCode < 0 || r.ExitCode > 255 || r.HeadBytes < 0 || r.TailBytes < 0 || outputRetained(r) != len(data) || r.FailureBytes < 0 || r.FailureBytes > 8*1024 || r.HeadBytes > len(data) || r.TailBytes > len(data) || r.FailureBytes > len(data) || r.FailureOffset < 0 || (r.FailureBytes == 0 && r.FailureOffset != 0) || (r.FailureBytes > 0 && (r.ExitCode == 0 || r.FailureOffset < int64(r.HeadBytes) || r.FailureOffset > r.TotalBytes-int64(r.FailureBytes)-int64(r.TailBytes))) || r.TotalBytes < int64(len(data)) || r.OmittedBytes != r.TotalBytes-int64(len(data)) || r.Truncated != (r.OmittedBytes > 0) {
				return fmt.Errorf("invalid command output evidence")
			}
			retained += len(data)
		}
		return nil
	}
	if err := validate(j.Evidence.Output); err != nil {
		return err
	}
	for _, r := range append(append(append([]VerificationResult(nil), j.Verification...), j.FormatterPreparation...), j.Diagnostics...) {
		if err := validate(r.Output); err != nil {
			return err
		}
	}
	if retained > commandOutputBudget {
		return fmt.Errorf("command output evidence exceeds aggregate budget")
	}
	return nil
}
