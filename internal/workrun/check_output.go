package workrun

import (
	"encoding/base64"
	"fmt"
	"io"
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
	limit         int
	head, tail    []byte
	result        CommandOutput
	releaseUnused func(int)
}

func (b *boundedCommandOutput) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	n := len(p)
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
func (b *boundedCommandOutput) completed(exit int) {
	b.Lock()
	defer b.Unlock()
	if !b.result.Completed && b.releaseUnused != nil {
		b.releaseUnused(b.limit - len(b.head) - len(b.tail))
	}
	b.result.Completed = true
	b.result.ExitCode = exit
}
func (b *boundedCommandOutput) snapshot() CommandOutput {
	b.Lock()
	defer b.Unlock()
	r := b.result
	r.HeadBytes = len(b.head)
	r.TailBytes = len(b.tail)
	setCommandOutputData(&r, append(append([]byte(nil), b.head...), b.tail...))
	r.OmittedBytes = r.TotalBytes - int64(len(b.head)+len(b.tail))
	r.Truncated = r.OmittedBytes > 0
	return r
}
func trimCommandOutput(r CommandOutput, limit int) CommandOutput {
	data, _ := commandOutputBytes(r)
	if len(data) <= limit {
		return r
	}
	head := (limit + 1) / 2
	tail := limit - head

	if head > r.HeadBytes {
		head = r.HeadBytes
		tail = limit - head
	}
	if tail > r.TailBytes {
		tail = r.TailBytes
		head = limit - tail
	}
	setCommandOutputData(&r, append(append([]byte(nil), data[:head]...), data[len(data)-tail:]...))
	r.HeadBytes = head
	r.TailBytes = tail
	r.OmittedBytes = r.TotalBytes - int64(head+tail)
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
	for i := range j.Verification {
		for k := range j.Verification[i].Output {
			outputs = append(outputs, &j.Verification[i].Output[k])
		}
	}
	for _, output := range outputs {
		retained += output.HeadBytes + output.TailBytes
	}
	if retained <= budget {
		return
	}
	// Preserve short results and allocate their unused shares to longer output.
	sort.Slice(outputs, func(i, k int) bool {
		return outputs[i].HeadBytes+outputs[i].TailBytes < outputs[k].HeadBytes+outputs[k].TailBytes
	})
	for i, output := range outputs {
		*output = trimCommandOutput(*output, budget/(len(outputs)-i))
		budget -= output.HeadBytes + output.TailBytes
	}
}
func effectiveChecks(j *Journal) [][]string {
	if j.Plan.CheckSelection == "runtime" {
		return j.EffectiveChecks
	}
	return j.Plan.Checks
}

func invalidateCommandEvidence(j *Journal) {
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
			if err != nil || !r.Completed || r.Command < 1 || r.Command > 64 || (r.Phase != "candidate" && r.Phase != "baseline") || r.ExitCode < 0 || r.ExitCode > 255 || r.HeadBytes < 0 || r.TailBytes < 0 || r.HeadBytes+r.TailBytes != len(data) || r.TotalBytes < int64(len(data)) || r.OmittedBytes != r.TotalBytes-int64(len(data)) || r.Truncated != (r.OmittedBytes > 0) {
				return fmt.Errorf("invalid command output evidence")
			}
			retained += len(data)
		}
		return nil
	}
	if err := validate(j.Evidence.Output); err != nil {
		return err
	}
	for _, r := range j.Verification {
		if err := validate(r.Output); err != nil {
			return err
		}
	}
	if retained > commandOutputBudget {
		return fmt.Errorf("command output evidence exceeds aggregate budget")
	}
	return nil
}
