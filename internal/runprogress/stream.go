package runprogress

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sync"
)

// Stream accepts arbitrary chunks and emits complete bounded presentation lines.
// Native streams decode completed messages, rather than repeating token deltas.
type Stream struct {
	mu       sync.Mutex
	recorder *Recorder
	base     Event
	native   bool
	pending  []byte
	dropping bool
	err      error
}

func NewStream(r *Recorder, base Event, native bool) *Stream {
	return &Stream{recorder: r, base: base, native: native}
}
func (s *Stream) emit(line []byte) error {
	texts := []string{string(line)}
	if s.native {
		texts = nativeTexts(line)
	}
	for _, text := range texts {
		if text == "" {
			continue
		}
		e := s.base
		e.Text = s.base.Text + text
		if err := s.recorder.Record(e); err != nil {
			return err
		}
	}
	return nil
}
func (s *Stream) Write(data []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return 0, s.err
	}
	n := len(data)
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		part := data
		if i >= 0 {
			part = data[:i]
		}
		if !s.dropping {
			limit := textLimit
			if s.native {
				limit = 8 * 1024 * 1024
			}
			if len(s.pending)+len(part) > limit {
				if s.native {
					s.err = s.emit([]byte("[output line exceeds progress limit; see private raw log]"))
				} else {
					s.pending = append(s.pending, part[:limit-len(s.pending)]...)
					s.err = s.emit(append(s.pending, []byte(" [truncated]")...))
				}
				s.pending = nil
				s.dropping = true
			} else {
				s.pending = append(s.pending, part...)
			}
		}
		if i < 0 {
			break
		}
		if !s.dropping && s.err == nil {
			s.err = s.emit(s.pending)
		}
		s.pending = nil
		s.dropping = false
		data = data[i+1:]
		if s.err != nil {
			return n, s.err
		}
	}
	return n, s.err
}
func (s *Stream) Finish() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err == nil && len(s.pending) > 0 {
		s.err = s.emit(s.pending)
	}
	s.pending = nil
	return s.err
}
func nativeTexts(line []byte) []string {
	var e map[string]json.RawMessage
	if json.Unmarshal(line, &e) != nil {
		return []string{clip(string(line))}
	}
	str := func(m map[string]json.RawMessage, k string) string { var v string; json.Unmarshal(m[k], &v); return v }
	kind := str(e, "type")
	switch kind {
	case "item.completed", "item.started":
		var item map[string]json.RawMessage
		json.Unmarshal(e["item"], &item)
		typ := str(item, "type")
		if typ == "collab_tool_call" {
			tool := str(item, "tool")
			switch tool {
			case "spawn", "spawn_agent", "wait", "wait_agent", "send_message", "list_agents", "resume_agent", "close_agent":
				state := "completed"
				if kind == "item.started" {
					state = "started"
				}
				return []string{"Agent tool " + state + ": " + tool}
			}
		}
		if kind == "item.started" {
			if typ == "command_execution" {
				return []string{"Command: " + str(item, "command")}
			}
			return nil
		}
		switch typ {
		case "agent_message", "reasoning":
			return []string{str(item, "text")}
		case "command_execution":
			status := "Command finished"
			var code int
			if raw := item["exit_code"]; len(raw) > 0 && string(raw) != "null" && json.Unmarshal(raw, &code) == nil {
				status = fmt.Sprintf("Command finished (exit %d)", code)
			}
			if command := str(item, "command"); command != "" {
				status += ": " + command
			}
			return []string{status, str(item, "aggregated_output")}
		case "mcp_tool_call", "web_search", "file_change":
			return []string{"Completed " + typ}
		default:
			return []string{"Completed native item: " + typ}
		}
	case "assistant", "user":
		var message struct {
			Content []struct {
				Type    string                     `json:"type"`
				Text    string                     `json:"text"`
				Name    string                     `json:"name"`
				Content json.RawMessage            `json:"content"`
				Input   map[string]json.RawMessage `json:"input"`
			}
		}
		json.Unmarshal(e["message"], &message)
		var texts []string
		for _, block := range message.Content {
			switch block.Type {
			case "text":
				texts = append(texts, block.Text)
			case "tool_use":
				label := "Tool: " + block.Name
				if block.Name == "Bash" {
					if command := str(block.Input, "command"); command != "" {
						label += ": " + command
					}
				}
				texts = append(texts, label)
			case "tool_result":
				var text string
				if json.Unmarshal(block.Content, &text) == nil {
					texts = append(texts, text)
				} else {
					var parts []struct {
						Text string `json:"text"`
					}
					json.Unmarshal(block.Content, &parts)
					for _, part := range parts {
						texts = append(texts, part.Text)
					}
				}
			}
		}
		return texts
	case "result":
		return []string{str(e, "result")}
	case "error":
		return []string{"Native error: " + str(e, "message")}
	case "thread.started", "turn.started", "turn.completed", "system":
		return []string{"Native event: " + kind}
	case "item.updated", "stream_event", "content_block_delta", "message_delta":
		return nil
	default:
		if kind == "" {
			return []string{"Native structured event"}
		}
		return []string{fmt.Sprintf("Native event: %s", kind)}
	}
}

// Summarize extracts bounded human-readable text from a native event.
func Summarize(provider string, line []byte) []string {
	texts := nativeTexts(line)
	for i := range texts {
		texts[i] = clip(texts[i])
	}
	return texts
}
