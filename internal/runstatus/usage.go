package runstatus

import (
	"bytes"
	"github.com/tjpeel/sdlc/internal/runusage"
)

const maxEventLine = 256 * 1024
const maxTokenCount int64 = 1_000_000_000_000

type Usage = runusage.Usage
type TokenUsage = runusage.TokenUsage
type ContextUsage = runusage.ContextUsage

// The pinned Codex 0.159.3 emitter uses ThreadTokenUsage.total here:
// https://github.com/openai/codex/blob/rust-v0.159.3/codex-rs/exec/src/event_processor_with_jsonl_output.rs
// Claude primary assistant usage and result modelUsage are native wire fields:
// https://github.com/anthropics/claude-agent-sdk-python/blob/main/src/claude_agent_sdk/types.py
// Claude documents input + cache read + cache creation as current input context:
// https://code.claude.com/docs/en/statusline
// The Claude 2.1.287 CLI may omit contextWindow: no window or percentage is
// inferred from model names, aggregate usage, or a provider's advertised limit.
type usageObserver struct {
	line                []byte
	dropping            bool
	shared              *runusage.Observer
	provider, requested string
}

func (o *usageObserver) reset() {
	clear(o.line)
	*o = usageObserver{}
}

func (o *usageObserver) feed(data []byte, provider, model string, usage *Usage) {
	for len(data) > 0 {
		at := bytes.IndexByte(data, '\n')
		part := data
		if at >= 0 {
			part = data[:at]
		}
		if !o.dropping {
			if len(o.line)+len(part) > maxEventLine {
				clear(o.line)
				o.line = nil
				o.dropping = true
			} else {
				o.line = append(o.line, part...)
			}
		}
		if at < 0 {
			return
		}
		if !o.dropping {
			o.consume(o.line, provider, model, usage)
		}
		clear(o.line)
		o.line = o.line[:0]
		o.dropping = false
		data = data[at+1:]
	}
}

func (o *usageObserver) consume(line []byte, provider, requested string, usage *Usage) {
	if o.shared == nil || o.provider != provider || o.requested != requested {
		o.shared = runusage.NewObserver(provider, requested)
		o.provider = provider
		o.requested = requested
	}
	o.shared.Consume(line)
	*usage = o.shared.Snapshot()
}
