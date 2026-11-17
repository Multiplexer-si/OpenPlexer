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
				s.WriteString(str(block["text"]) + "\n")
			case "tool_result":
				t.WriteString(str(block["content"]) + "\n")
			}
		}
	})
	return s.String(), t.String()
}

// Codex prints a hook denial on stderr, and a command's output as an item.
func codexFeedback(stdout, stderr []byte) (said, toolResults string) {
	var s, t strings.Builder
	t.Write(stderr)
	eachLine(stdout, func(ev map[string]any) {
		item, _ := ev["item"].(map[string]any)
		switch item["type"] {
		case "agent_message":
			s.WriteString(str(item["text"]) + "\n")
		case "command_execution", "file_change", "mcp_tool_call":
			b, _ := json.Marshal(item)
			t.Write(b)
		}
	})
	return s.String(), t.String()
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}
