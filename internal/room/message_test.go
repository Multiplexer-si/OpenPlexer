package room

import (
	"reflect"
	"strings"
	"testing"
)

func TestEventMessage(t *testing.T) {
	text := eventPayload("status", 7, `{"summary":"<&>"}`)
	m := EventMessage(mustEvent(t, text))
	parsed, err := ParseMessage(mustMarshal(t, m))
	if err != nil {
		t.Fatal(err)
	}
	seq, hasSeq := parsed.Seq()
	if parsed.V() != 1 || parsed.Type() != "event" || parsed.Room() != "mis" || !hasSeq || seq != 7 || parsed.Sender() != (Sender{ID: "ses", Kind: Daemon}) {
		t.Errorf("message = %s", mustMarshal(t, parsed))
	}
	ev, ok := parsed.Payload().(Event)
	if !ok || string(mustMarshal(t, ev)) != text {
		t.Errorf("payload = %+v, want the event text %s", parsed.Payload(), text)
	}
	for _, field := range []string{`"event_id":"e7"`, `"ts":"2026-01-15T12:00:00.000Z"`} {
		if !strings.Contains(string(mustMarshal(t, m)), field) {
			t.Errorf("message lacks %s", field)
		}
	}
}

func TestEventMessageOfAPersonIsFromTheDashboard(t *testing.T) {
	text := `{"id":"e1","seq":1,"mission":"mis","type":"human","agent":{"id":"mem","vendor":"human"},"on_behalf_of":"mem","at":"t","body":{"text":"hi"}}`
	if got := EventMessage(mustEvent(t, text)).Sender(); got != (Sender{ID: "mem", Kind: Dashboard}) {
		t.Errorf("Sender = %+v", got)
	}
}

func TestResumeMessage(t *testing.T) {
	m := ResumeMessage(Sender{ID: "dmn", Kind: Daemon}, "mis", 41)
	parsed, err := ParseMessage(mustMarshal(t, m))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Type() != "resume" || parsed.Room() != "mis" || parsed.Sender() != (Sender{ID: "dmn", Kind: Daemon}) || parsed.Payload() != (Resume{LastSeq: 41}) {
		t.Errorf("message = %s", mustMarshal(t, parsed))
	}
}

func TestErrorMessage(t *testing.T) {
	hub := Sender{ID: "hub", Kind: Server}
	answer := Error{Code: RateLimit, Message: "slow down", ClientEventID: "cev-1"}
	m := ErrorMessage(hub, "mis", "2026-01-15T12:00:00.000Z", answer)
	parsed, err := ParseMessage(mustMarshal(t, m))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Type() != "error" || parsed.Room() != "mis" || parsed.Sender() != hub || parsed.Payload() != answer {
		t.Errorf("message = %s", mustMarshal(t, parsed))
	}
	bare := ErrorMessage(hub, "", "2026-01-15T12:00:00.000Z", Error{Code: Behind, Message: "x"})
	if bare.Room() != "" || strings.Contains(string(mustMarshal(t, bare)), `"room"`) || strings.Contains(string(mustMarshal(t, bare)), "client_event_id") {
		t.Errorf("message = %s", mustMarshal(t, bare))
	}
}

func TestBuildersPanicOnAnInvalidArgument(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("ResumeMessage with no room did not panic")
		}
	}()
	ResumeMessage(Sender{ID: "dmn", Kind: Daemon}, "", 0)
}

func TestMessageKeepsUnknownFieldsAtEveryLevel(t *testing.T) {
	text := `{"v":1,"type":"resume","sender":{"id":"d","kind":"daemon","x":1},"payload":{"last_seq":3.0,"y":[1,2.50]},"z":{"w":null}}`
	m, err := ParseMessage([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	if got := string(mustMarshal(t, m)); got != text {
		t.Errorf("MarshalJSON = %s, want %s", got, text)
	}
	event := `{"v":1,"type":"event","sender":{"id":"d","kind":"daemon"},"payload":` + eventPayload("status", 1, `{"summary":"s","k":1.0}`) + `,"extra":true}`
	m, err = ParseMessage([]byte(event))
	if err != nil || string(mustMarshal(t, m)) != event {
		t.Errorf("MarshalJSON = %s, %v; want %s", mustMarshal(t, m), err, event)
	}
}

func TestMessageOfAnUndefinedTypeKeepsItsPayload(t *testing.T) {
	m, err := ParseMessage([]byte(`{"v":2,"type":"hello","sender":{"id":"d","kind":"server"},"payload":{ "k" : 1 }}`))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := m.Payload(), (Other{Raw: []byte(`{"k":1}`)}); !reflect.DeepEqual(got, want) {
		t.Errorf("Payload = %+v, want %+v", got, want)
	}
}

func TestParseMessageNeedsAPayloadOfItsType(t *testing.T) {
	for _, text := range []string{
		`{"v":1,"type":"resume","sender":{"id":"d","kind":"daemon"}}`,
		`{"v":1,"type":"resume","sender":{"id":"d","kind":"daemon"},"payload":null}`,
		`{"v":1,"type":"error","sender":{"id":"d","kind":"daemon"},"payload":{"code":1,"message":"x"}}`,
		`{"v":1,"type":"event","sender":{"id":"d","kind":"daemon"},"payload":{}}`,
	} {
		_, err := ParseMessage([]byte(text))
		invalid, ok := err.(*ValidationError)
		if !ok || len(invalid.Issues) == 0 || !strings.HasPrefix(invalid.Issues[0].Path, "payload") {
			t.Errorf("ParseMessage(%s) = %v", text, err)
		}
	}
}

func mustMarshal(t *testing.T, v interface{ MarshalJSON() ([]byte, error) }) []byte {
	t.Helper()
	text, err := v.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return text
}
