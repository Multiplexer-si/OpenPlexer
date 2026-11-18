package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"strings"
)

func eachLine(raw []byte, fn func(map[string]any)) {
	sc := bufio.NewScanner(bytes.NewReader(raw))
	sc.Buffer(nil, 16<<20)
	for sc.Scan() {
		var ev map[string]any
		if json.Unmarshal(sc.Bytes(), &ev) == nil {
			fn(ev)
		}
	}
}

func claudeFeedback(stdout, _ []byte) (said, toolResults string) {
	var s, t strings.Builder
	eachLine(stdout, func(ev map[string]any) {
		msg, _ := ev["message"].(map[string]any)
		blocks, _ := msg["content"].([]any)
		for _, b := range blocks {
			block, _ := b.(map[string]any)
			switch block["type"] {
			case "text":
				s.WriteString(texts(block["text"]))
			case "tool_result":
				t.WriteString(texts(block["content"]))
			}
		}
	})
	return s.String(), t.String()
}

// Codex 0.169.0 logs the reason of a blocked call at error level on standard
// error and leaves it out of its JSON output. That text is the tool error that
// the model gets.
func codexFeedback(stdout, stderr []byte) (said, toolResults string) {
	var s, t strings.Builder
	eachLine(stdout, func(ev map[string]any) {
		item, _ := ev["item"].(map[string]any)
		switch item["type"] {
		case "agent_message":
			s.WriteString(texts(item["text"]))
		case "command_execution", "file_change", "mcp_tool_call":
			t.WriteString(texts(item))
		}
	})
	t.Write(stderr)
	return s.String(), t.String()
}

// texts returns every string in a decoded JSON value, one on each line, so a
// reason is matched as the agent printed it and not as JSON escapes it.
func texts(v any) string {
	var b strings.Builder
	var walk func(any)
	walk = func(v any) {
		switch v := v.(type) {
		case string:
			b.WriteString(v + "\n")
		case []any:
			for _, item := range v {
				walk(item)
			}
		case map[string]any:
			for _, k := range sortedKeys(v) {
				walk(v[k])
			}
		}
	}
	walk(v)
	return b.String()
}
