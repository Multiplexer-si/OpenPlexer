package room

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

const claimEventText = `{"id":"e1","seq":%SEQ%,"mission":"mis","type":"claim","agent":{"id":"a","vendor":"v","parent":"root","extra":1},"on_behalf_of":"m","at":"2026-01-15T12:00:00.000Z","body":{"item":"i","paths":["a/**"],"intent":"x","ttl_seconds":%TTL%,"thread_id":"t","future":{"k":[1,2.50]}},"later":true}`

func claimEvent(seq, ttl string) string {
	return strings.NewReplacer("%SEQ%", seq, "%TTL%", ttl).Replace(claimEventText)
}

func TestEventAccessorsAndBody(t *testing.T) {
	ev := mustEvent(t, claimEvent("7", "60"))
	if ev.ID() != "e1" || ev.Seq() != 7 || ev.Mission() != "mis" || ev.Type() != "claim" || ev.OnBehalfOf() != "m" || ev.At() != "2026-01-15T12:00:00.000Z" {
		t.Errorf("accessors: %+v", ev)
	}
	if want := (Agent{ID: "a", Vendor: "v", Parent: "root"}); ev.Agent() != want {
		t.Errorf("Agent = %+v, want %+v", ev.Agent(), want)
	}
	want := &Claim{Thread: Thread{ThreadID: "t"}, Item: "i", Paths: []string{"a/**"}, Intent: "x", TTLSeconds: 60}
	if got := ev.Body(); !reflect.DeepEqual(got, want) {
		t.Errorf("Body = %+v, want %+v", got, want)
	}
}

func TestBodyIsFreshOnEachCall(t *testing.T) {
	ev := mustEvent(t, claimEvent("7", "60"))
	ev.Body().(*Claim).Paths[0] = "changed"
	if got := ev.Body().(*Claim).Paths[0]; got != "a/**" {
		t.Errorf("a change to one Body reached the next: %q", got)
	}
}

func TestEventWritesBackUnknownFieldsAndNumbers(t *testing.T) {
	text := claimEvent("7", "60")
	event := mustEvent(t, " "+claimEvent("7.0", "6e1")+"\n")
	assertSameJSON(t, mustMarshal(t, event), text)
}

func TestEventNumbers(t *testing.T) {
	for _, c := range []struct {
		seq, ttl string
		valid    bool
	}{
		{"1.0", "60", true},
		{"1.0000000000000001", "60", true},
		{"1.5", "60", false},
		{"1e400", "60", false},
		{"9007199254740991", "60", true},
		{"9007199254740992", "60", false},
		{"-1", "60", false},
		{"0", "60", true},
		{"1", "0", false},
		{"1", "9007199254740991", true},
		{"1", "6e1", true},
		{"1", "1e400", false},
	} {
		_, err := ParseEvent([]byte(claimEvent(c.seq, c.ttl)))
		if (err == nil) != c.valid {
			t.Errorf("seq %s, ttl_seconds %s: %v, want valid = %v", c.seq, c.ttl, err, c.valid)
		}
	}
}

func TestEventLastDuplicateKeyWins(t *testing.T) {
	ev := mustEvent(t, strings.Replace(claimEvent("7", "60"), `"seq":7`, `"seq":"x","seq":8`, 1))
	if ev.Seq() != 8 {
		t.Errorf("Seq = %d, want 8", ev.Seq())
	}
}

func TestAuditRoomSerialization(t *testing.T) {
	for _, tc := range []struct {
		kind, field, body string
	}{
		{"claim", "intent", `"item":"i","paths":["src/**"],"ttl_seconds":60`},
		{"decision", "title", `"detail":"d"`},
		{"status", "summary", `"plan_step":"p"`},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			body := `{"` + tc.field + `":"unchecked","` + tc.field + `":"approved",` + tc.body + `,"future":[2.50]}`
			event := mustEvent(t, eventPayload(tc.kind, 1, body))
			var stdout bytes.Buffer
			if err := json.NewEncoder(&stdout).Encode(event); err != nil {
				t.Fatal(err)
			}
			got := stdout.String()
			if strings.Contains(got, "unchecked") || strings.Count(got, `"`+tc.field+`":`) != 1 ||
				!strings.Contains(got, `"`+tc.field+`":"approved"`) {
				t.Errorf("encoded %s = %s, want only the validated value", tc.field, got)
			}
			if !strings.Contains(got, `"future":[2.50]`) {
				t.Errorf("encoded %s lost its extension: %s", tc.kind, got)
			}
		})
	}
	t.Run("invalid UTF-8", func(t *testing.T) {
		text := eventPayload("decision", 1, "{\"title\":\"\xff\",\"detail\":\"d\"}")
		if _, err := ParseEvent([]byte(text)); err == nil {
			t.Error("ParseEvent accepted invalid UTF-8")
		}
		frame := `{"v":1,"type":"event",` + sender + `,"payload":` + text + `}`
		if _, err := ParseMessage([]byte(frame)); err == nil {
			t.Error("ParseMessage accepted invalid UTF-8")
		}
	})
}

func TestEventReportsEveryIssue(t *testing.T) {
	_, err := ParseEvent([]byte(`{"id":"","seq":-1,"mission":"m","type":"claim","agent":{"id":"a"},"on_behalf_of":"m","at":"t","body":{"paths":[1]}}`))
	invalid, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("error %v is not a ValidationError", err)
	}
	var paths []string
	for _, issue := range invalid.Issues {
		paths = append(paths, issue.Path)
	}
	want := []string{"id", "seq", "agent.vendor", "body.item", "body.paths[0]", "body.intent", "body.ttl_seconds"}
	if !reflect.DeepEqual(paths, want) {
		t.Errorf("issue paths = %v, want %v", paths, want)
	}
}

func TestParseEventRejectsWhatIsNotAnObject(t *testing.T) {
	for _, text := range []string{``, `null`, `[]`, `{} {}`, `{"id"`} {
		if _, err := ParseEvent([]byte(text)); err == nil {
			t.Errorf("ParseEvent(%q) is valid", text)
		}
	}
}

func TestBlankText(t *testing.T) {
	for _, c := range []struct {
		text  string
		valid bool
	}{
		{"hi", true},
		{" \\u0085 ", true},
		{"\\u180e", true},
		{"\\u200b", true},
		{" \\t\\n\\u00a0\\u3000\\ufeff", false},
		{"", false},
	} {
		_, err := ParseEvent([]byte(`{"id":"e","seq":1,"mission":"m","type":"question","agent":{"id":"a","vendor":"v"},"on_behalf_of":"m","at":"t","body":{"text":"` + c.text + `"}}`))
		if (err == nil) != c.valid {
			t.Errorf("text %q: %v, want valid = %v", c.text, err, c.valid)
		}
	}
}
