package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

var (
	uuidRe      = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	timestampRe = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:?\d{2})?`)
	toolIDRe    = regexp.MustCompile(`\b(toolu|call|fc|rs|msg)_[A-Za-z0-9]{8,}`)
	rolloutRe   = regexp.MustCompile(`rollout-[^"\s/]+`)
	uidDirRe    = regexp.MustCompile(`claude-\d+`)
)

var idKeys = map[string]string{
	"session_id":  "<SESSION_ID>",
	"prompt_id":   "<PROMPT_ID>",
	"turn_id":     "<TURN_ID>",
	"tool_use_id": "<TOOL_USE_ID>",
	"agent_id":    "<AGENT_ID>",
}

type redactor struct {
	replacements [][2]string
	secrets      []string
}

func newRedactor(projectDirs ...string) (*redactor, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	r := &redactor{}
	var pairs [][2]string
	for _, p := range projectDirs {
		pairs = append(pairs, [2]string{p, "<PROJECT>"}, [2]string{strings.ReplaceAll(p, "/", "-"), "<PROJECT_SLUG>"})
		if real, err := filepath.EvalSymlinks(p); err == nil {
			pairs = append(pairs, [2]string{real, "<PROJECT>"})
		}
		if strings.HasPrefix(p, "/private/") {
			pairs = append(pairs, [2]string{strings.TrimPrefix(p, "/private"), "<PROJECT>"})
		}
	}
	pairs = append(pairs, [2]string{home, "<HOME>"})
	r.secrets = append(r.secrets, home)
	if u, err := user.Current(); err == nil {
		for _, name := range []string{u.Username, filepath.Base(home)} {
			if name != "" {
				pairs = append(pairs, [2]string{name, "<USER>"})
				r.secrets = append(r.secrets, name)
			}
		}
	}
	r.secrets = append(r.secrets, projectDirs...)
	slices.SortStableFunc(pairs, func(a, b [2]string) int { return len(b[0]) - len(a[0]) })
	r.replacements = pairs
	return r, nil
}

func (r *redactor) text(s string) string {
	for _, p := range r.replacements {
		s = strings.ReplaceAll(s, p[0], p[1])
	}
	s = rolloutRe.ReplaceAllString(s, "<ROLLOUT>")
	s = uidDirRe.ReplaceAllString(s, "claude-<UID>")
	s = uuidRe.ReplaceAllString(s, "<UUID>")
	s = timestampRe.ReplaceAllString(s, "<TIMESTAMP>")
	return toolIDRe.ReplaceAllString(s, "<TOOL_USE_ID>")
}

func (r *redactor) value(key string, v any) any {
	switch x := v.(type) {
	case string:
		if placeholder, ok := idKeys[key]; ok && x != "" {
			return placeholder
		}
		return r.text(x)
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[r.text(k)] = r.value(k, e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = r.value(key, e)
		}
		return out
	default:
		return v
	}
}

func (r *redactor) json(raw []byte) ([]byte, error) {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r.value("", v)); err != nil {
		return nil, err
	}
	out := buf.Bytes()
	for _, s := range r.secrets {
		if s != "" && strings.Contains(string(out), s) {
			return nil, fmt.Errorf("redacted sample still holds %q", s)
		}
	}
	return out, nil
}

func writeSamples(dir, agentName string, recs []record, project string, written map[string]bool) error {
	r, err := newRedactor(project)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, agentName), 0o755); err != nil {
		return err
	}
	for _, rec := range recs {
		name := rec.Event
		var in struct {
			Tool string `json:"tool_name"`
		}
		json.Unmarshal(rec.Input, &in)
		if in.Tool != "" {
			name += "." + in.Tool
		}
		if written[agentName+"/"+name] {
			continue
		}
		written[agentName+"/"+name] = true
		out, err := r.json(rec.Input)
		if err != nil {
			return fmt.Errorf("%s %s: %w", agentName, name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, agentName, name+".json"), out, 0o644); err != nil {
			return err
		}
	}
	return nil
}
