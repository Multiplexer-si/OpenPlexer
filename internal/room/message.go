package room

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Message is one envelope of the WebSocket connection, with its payload read by
// type. It is immutable and keeps the text it was read from, so MarshalJSON
// writes unknown fields back.
type Message struct {
	raw     json.RawMessage
	v       int64
	typ     string
	room    string
	seq     int64
	hasSeq  bool
	sender  Sender
	payload Payload
}

// Payload is an Event, Resume, Error, or Other.
type Payload interface{ payload() }

type Resume struct{ LastSeq int64 }

type Error struct {
	Code          ErrorCode
	Message       string
	ClientEventID string
}

// Other is the payload of a type that version 1 does not define, such as hello.
type Other struct{ Raw json.RawMessage }

func (Event) payload()  {}
func (Resume) payload() {}
func (Error) payload()  {}
func (Other) payload()  {}

// ParseMessage reads a message and validates it against
// spec/room/v1/envelope.schema.json and, by type, the payload schemas. The
// error is a *ValidationError.
func ParseMessage(text []byte) (Message, error) {
	return readMessage(text, true)
}

// readMessage with versionOne false checks only what the envelope schema
// checks: the payload of an event, and no other payload.
func readMessage(text []byte, versionOne bool) (Message, error) {
	env, issues := readEnvelope(text)
	var payload Payload
	if len(issues) == 0 {
		payload, issues = env.readPayload(versionOne)
	}
	if len(issues) > 0 {
		return Message{}, &ValidationError{Issues: issues}
	}
	return env.message(payload), nil
}

func (e envelope) message(payload Payload) Message {
	return Message{
		raw: compact(e.text), v: e.v, typ: e.typ, room: e.room,
		seq: e.seq, hasSeq: e.hasSeq, sender: e.sender, payload: payload,
	}
}

func (m Message) V() int64           { return m.v }
func (m Message) Type() string       { return m.typ }
func (m Message) Room() string       { return m.room }
func (m Message) Seq() (int64, bool) { return m.seq, m.hasSeq }
func (m Message) Sender() Sender     { return m.sender }
func (m Message) Payload() Payload   { return m.payload }

func (m Message) MarshalJSON() ([]byte, error) {
	if m.raw == nil {
		return nil, &ValidationError{Issues: []Issue{{Reason: "is the zero Message"}}}
	}
	return m.raw, nil
}

func (r *reader) resume(doc any) Resume {
	var p Resume
	if o, ok := r.object("", doc); ok {
		p.LastSeq, _ = field(r, "", o, "last_seq", required, r.integerFrom(0))
	}
	return p
}

func (r *reader) errorPayload(doc any) Error {
	var p Error
	if o, ok := r.object("", doc); ok {
		code, _ := field(r, "", o, "code", required, r.text)
		p.Code = ErrorCode(code)
		p.Message, _ = field(r, "", o, "message", required, r.text)
		p.ClientEventID, _ = field(r, "", o, "client_event_id", optional, r.text)
	}
	return p
}

// EventMessage carries ev. Room, seq, event_id, ts, and sender all come from
// ev, so they cannot disagree with it: the sender is the poster of the event,
// a dashboard for a person and a daemon for an agent session.
func EventMessage(ev Event) Message {
	kind := Daemon
	if ev.agent.Vendor == "human" {
		kind = Dashboard
	}
	return build(object{
		"v": 1, "type": "event", "room": ev.mission, "seq": ev.seq, "event_id": ev.id, "ts": ev.at,
		"sender":  object{"id": ev.agent.ID, "kind": kind},
		"payload": ev.raw,
	})
}

// ResumeMessage asks for the events of room after lastSeq. It panics when
// from or room is empty or lastSeq is negative.
func ResumeMessage(from Sender, room string, lastSeq int64) Message {
	return build(object{
		"v": 1, "type": "resume", "room": room,
		"sender":  object{"id": from.ID, "kind": from.Kind},
		"payload": object{"last_seq": lastSeq},
	})
}

// ErrorMessage reports e. A room of "" leaves room out. It panics when from or
// ts is empty.
func ErrorMessage(from Sender, room, ts string, e Error) Message {
	payload := object{"code": e.Code, "message": e.Message}
	if e.ClientEventID != "" {
		payload["client_event_id"] = e.ClientEventID
	}
	m := object{
		"v": 1, "type": "error", "ts": ts,
		"sender":  object{"id": from.ID, "kind": from.Kind},
		"payload": payload,
	}
	if room != "" {
		m["room"] = room
	}
	return build(m)
}

// build writes m and reads it back, so a built message passes the checks that a
// received one does.
func build(m object) Message {
	var text bytes.Buffer
	encoder := json.NewEncoder(&text)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(m); err != nil {
		panic(fmt.Sprintf("room: cannot write a message: %v", err))
	}
	msg, err := ParseMessage(text.Bytes())
	if err != nil {
		panic(fmt.Sprintf("room: not a valid message: %v", err))
	}
	return msg
}
