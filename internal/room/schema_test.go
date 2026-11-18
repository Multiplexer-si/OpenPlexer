package room

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func mustEvent(t *testing.T, text string) Event {
	t.Helper()
	ev, err := ParseEvent([]byte(text))
	if err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return ev
}

func TestEventTypesEqualTheSchemaEnum(t *testing.T) {
	var schema struct {
		Properties struct {
			Type struct {
				Enum []string `json:"enum"`
			} `json:"type"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(readFile(t, specDir+"/room-event.schema.json"), &schema); err != nil {
		t.Fatal(err)
	}
	if got := eventTypeNames(); !slices.Equal(got, schema.Properties.Type.Enum) {
		t.Errorf("eventTypes = %v, the schema enum = %v", got, schema.Properties.Type.Enum)
	}
}

func TestExamplesParseAndWriteBack(t *testing.T) {
	parsers := map[string]func(text []byte) ([]byte, error){
		"room-event": func(text []byte) ([]byte, error) {
			ev, err := ParseEvent(text)
			if err != nil {
				return nil, err
			}
			return ev.MarshalJSON()
		},
		"envelope": func(text []byte) ([]byte, error) {
			msg, err := ParseMessage(text)
			if err != nil {
				return nil, err
			}
			return msg.MarshalJSON()
		},
		"resume": func(text []byte) ([]byte, error) {
			_, err := parse(text, (*reader).resume)
			return compact(text), err
		},
		"error": func(text []byte) ([]byte, error) {
			_, err := parse(text, (*reader).errorPayload)
			return compact(text), err
		},
	}
	dirs, err := filepath.Glob(filepath.Join(specDir, "examples", "*"))
	if err != nil || len(dirs) == 0 {
		t.Fatalf("found no examples: %v", err)
	}
	for _, dir := range dirs {
		parser, ok := parsers[filepath.Base(dir)]
		if !ok {
			t.Fatalf("no parser for the examples in %s", dir)
		}
		files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
		for _, path := range files {
			t.Run(filepath.Base(dir)+"/"+filepath.Base(path), func(t *testing.T) {
				text, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				written, err := parser(text)
				if err != nil {
					t.Fatal(err)
				}
				var want bytes.Buffer
				if err := json.Compact(&want, text); err != nil {
					t.Fatal(err)
				}
				if !bytes.Equal(written, want.Bytes()) {
					t.Errorf("written back\n%s\nwant\n%s", written, want.Bytes())
				}
			})
		}
	}
}
