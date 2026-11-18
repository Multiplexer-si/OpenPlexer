package room

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"
)

const specDir = "../../spec/room/v1"

type fixtureFile struct {
	Operation string        `json:"operation"`
	Schema    string        `json:"schema"`
	Cases     []fixtureCase `json:"cases"`
}

type fixtureCase struct {
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
	Expected json.RawMessage `json:"expected"`
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	text, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return text
}

func decodeStrict(t *testing.T, text []byte, into any) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		t.Fatalf("%s: %v", text, err)
	}
}

var validators = map[string]func(text []byte) error{
	"urn:openplexer:spec:room:v1:room-event": func(text []byte) error { _, err := ParseEvent(text); return err },
	"urn:openplexer:spec:room:v1:envelope":   validateEnvelope,
	"urn:openplexer:spec:room:v1:resume":     func(text []byte) error { _, err := parse(text, (*reader).resume); return err },
	"urn:openplexer:spec:room:v1:error":      func(text []byte) error { _, err := parse(text, (*reader).errorPayload); return err },
}

// The envelope schema checks the payload of an event and of no other message.
func validateEnvelope(text []byte) error {
	env, issues := readEnvelope(text)
	if len(issues) == 0 && env.typ == "event" {
		_, issues = env.event()
	}
	if len(issues) == 0 && !env.hasPayload {
		issues = []Issue{{Path: "payload", Reason: "is required"}}
	}
	if len(issues) > 0 {
		return &ValidationError{Issues: issues}
	}
	return nil
}

var operations = map[string]func(t *testing.T, file fixtureFile) func(t *testing.T, c fixtureCase){
	"validate": func(t *testing.T, file fixtureFile) func(*testing.T, fixtureCase) {
		validate, ok := validators[file.Schema]
		if !ok {
			t.Fatalf("no validator for the schema %s", file.Schema)
		}
		return func(t *testing.T, c fixtureCase) { runValidate(t, validate, c) }
	},
	"patterns-overlap":  func(*testing.T, fixtureFile) func(*testing.T, fixtureCase) { return runPatternsOverlap },
	"path-sets-overlap": func(*testing.T, fixtureFile) func(*testing.T, fixtureCase) { return runPathSetsOverlap },
	"deliver":           func(*testing.T, fixtureFile) func(*testing.T, fixtureCase) { return runDeliver },
	"active-claims":     func(*testing.T, fixtureFile) func(*testing.T, fixtureCase) { return runActiveClaims },
}

func TestFixtures(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join(specDir, "fixtures", "*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("found no fixture under %s: %v", specDir, err)
	}
	for _, path := range paths {
		name := filepath.Base(path)
		t.Run(name, func(t *testing.T) {
			var file fixtureFile
			if err := json.Unmarshal(readFile(t, path), &file); err != nil || len(file.Cases) == 0 {
				t.Fatalf("%s: %d cases, %v", path, len(file.Cases), err)
			}
			prepare, ok := operations[file.Operation]
			if !ok {
				t.Fatalf("unknown operation %q", file.Operation)
			}
			run := prepare(t, file)
			for _, c := range file.Cases {
				t.Run(c.Name, func(t *testing.T) { run(t, c) })
			}
			t.Logf("%s: %d cases", name, len(file.Cases))
		})
	}
}

func runValidate(t *testing.T, validate func([]byte) error, c fixtureCase) {
	var expected struct {
		Valid *bool `json:"valid"`
	}
	decodeStrict(t, c.Expected, &expected)
	if expected.Valid == nil {
		t.Fatal("expected has no valid")
	}
	err := validate(c.Input)
	if valid := err == nil; valid != *expected.Valid {
		t.Fatalf("valid = %v, want %v (%v)", valid, *expected.Valid, err)
	}
	if err != nil {
		var invalid *ValidationError
		if !errors.As(err, &invalid) || len(invalid.Issues) == 0 {
			t.Fatalf("the error %q is not a ValidationError with an issue", err)
		}
		for _, issue := range invalid.Issues {
			if issue.Reason == "" {
				t.Errorf("the issue at %q has no reason", issue.Path)
			}
		}
	}
}

type overlapExpected struct {
	Overlap bool `json:"overlap"`
}

func runPatternsOverlap(t *testing.T, c fixtureCase) {
	var in struct{ A, B string }
	var want overlapExpected
	decodeStrict(t, c.Input, &in)
	decodeStrict(t, c.Expected, &want)
	if got := PatternsOverlap(in.A, in.B); got != want.Overlap {
		t.Errorf("PatternsOverlap = %v, want %v", got, want.Overlap)
	}
	if got := PatternsOverlap(in.B, in.A); got != want.Overlap {
		t.Errorf("PatternsOverlap reversed = %v, want %v", got, want.Overlap)
	}
}

func runPathSetsOverlap(t *testing.T, c fixtureCase) {
	var in struct{ A, B []string }
	var want overlapExpected
	decodeStrict(t, c.Input, &in)
	decodeStrict(t, c.Expected, &want)
	if got := PathsOverlap(in.A, in.B); got != want.Overlap {
		t.Errorf("PathsOverlap = %v, want %v", got, want.Overlap)
	}
}

func statusEvent(t *testing.T, seq int64) Event {
	t.Helper()
	return mustEvent(t, `{"id":"evt","seq":`+strconv.FormatInt(seq, 10)+`,"mission":"mis","type":"status","agent":{"id":"a","vendor":"v"},"on_behalf_of":"m","at":"2026-01-15T12:00:00.000Z","body":{"summary":"s"}}`)
}

func runDeliver(t *testing.T, c fixtureCase) {
	var in struct {
		Since int64
		Steps []struct {
			Step string
			Seq  int64
		}
	}
	var want struct {
		LastSeq int64   `json:"last_seq"`
		Sent    []int64 `json:"sent"`
	}
	decodeStrict(t, c.Input, &in)
	decodeStrict(t, c.Expected, &want)
	s := Subscribe(in.Since)
	sent := []int64{}
	for _, step := range in.Steps {
		var out []Event
		switch step.Step {
		case "replay":
			out = s.Replay(statusEvent(t, step.Seq))
		case "live":
			out = s.Live(statusEvent(t, step.Seq))
		case "go-live":
			out = s.GoLive()
		default:
			t.Fatalf("unknown step %q", step.Step)
		}
		for _, ev := range out {
			sent = append(sent, ev.Seq())
		}
	}
	if !slices.Equal(sent, want.Sent) || s.LastSeq() != want.LastSeq {
		t.Errorf("sent %v, last_seq %d; want %v, %d", sent, s.LastSeq(), want.Sent, want.LastSeq)
	}
}

type claimRow struct {
	AgentSessionID string   `json:"agent_session_id"`
	AgentVendor    string   `json:"agent_vendor"`
	ClaimID        string   `json:"claim_id"`
	ExpiresAt      string   `json:"expires_at"`
	Intent         string   `json:"intent"`
	Paths          []string `json:"paths"`
}

func runActiveClaims(t *testing.T, c fixtureCase) {
	var in struct {
		Events []json.RawMessage
		Now    string
	}
	var want struct {
		Claims []claimRow `json:"claims"`
	}
	decodeStrict(t, c.Input, &in)
	decodeStrict(t, c.Expected, &want)
	now, err := time.Parse(time.RFC3339, in.Now)
	if err != nil {
		t.Fatal(err)
	}
	var held []ActiveClaim
	for _, text := range in.Events {
		held = reduceClaim(held, mustEvent(t, string(text)))
	}
	got := []claimRow{}
	for _, claim := range active(held, now) {
		got = append(got, claimRow{
			AgentSessionID: claim.Holder.ID,
			AgentVendor:    claim.Holder.Vendor,
			ClaimID:        claim.ID,
			ExpiresAt:      claim.ExpiresAt.UTC().Format("2006-01-02T15:04:05.000Z"),
			Intent:         claim.Intent,
			Paths:          claim.Paths,
		})
	}
	if !reflect.DeepEqual(got, want.Claims) {
		t.Errorf("claims = %+v, want %+v", got, want.Claims)
	}
}
