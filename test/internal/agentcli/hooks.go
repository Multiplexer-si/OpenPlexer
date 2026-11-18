package agentcli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

func HooksOverrides(path string, wrap func(string) string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var file struct {
		Hooks map[string]any `json:"hooks"`
	}
	if err := dec.Decode(&file); err != nil {
		return nil, err
	}
	if len(file.Hooks) == 0 {
		return nil, fmt.Errorf("%s holds no hooks", path)
	}
	var args []string
	for _, event := range slices.Sorted(maps.Keys(file.Hooks)) {
		value := file.Hooks[event]
		if wrap != nil {
			wrapCommands(value, wrap)
		}
		rendered, err := toml(value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", event, err)
		}
		args = append(args, "-c", "hooks."+tomlKey(event)+"="+rendered)
	}
	return args, nil
}

func wrapCommands(v any, wrap func(string) string) {
	switch v := v.(type) {
	case map[string]any:
		for k, child := range v {
			if command, ok := child.(string); ok && k == "command" {
				v[k] = wrap(command)
				continue
			}
			wrapCommands(child, wrap)
		}
	case []any:
		for _, child := range v {
			wrapCommands(child, wrap)
		}
	}
}

var bareKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func tomlKey(k string) string {
	if bareKey.MatchString(k) {
		return k
	}
	return strconv.Quote(k)
}

func toml(v any) (string, error) {
	switch v := v.(type) {
	case string:
		return strconv.Quote(v), nil
	case bool:
		return strconv.FormatBool(v), nil
	case json.Number:
		return v.String(), nil
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			var err error
			if parts[i], err = toml(item); err != nil {
				return "", err
			}
		}
		return "[" + strings.Join(parts, ",") + "]", nil
	case map[string]any:
		parts := make([]string, 0, len(v))
		for _, k := range slices.Sorted(maps.Keys(v)) {
			item, err := toml(v[k])
			if err != nil {
				return "", err
			}
			parts = append(parts, tomlKey(k)+"="+item)
		}
		return "{" + strings.Join(parts, ",") + "}", nil
	}
	return "", fmt.Errorf("TOML has no inline form for %T", v)
}
