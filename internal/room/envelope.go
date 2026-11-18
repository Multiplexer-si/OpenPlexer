package room

import "encoding/json"

type SenderKind string

const (
	Daemon    SenderKind = "daemon"
	Dashboard SenderKind = "dashboard"
	Server    SenderKind = "server"
)

type Sender struct {
	ID   string
	Kind SenderKind
}

// envelope is the part of a message that envelope.schema.json describes, with
// the payload still unread. A Receiver reads the two parts apart, because it
// counts an event whose payload is not valid but whose envelope is.
type envelope struct {
	text       []byte
	v          int64
	typ        string
	room       string
	seq        int64
	hasSeq     bool
	sender     Sender
	payload    json.RawMessage
	hasPayload bool
}

func readEnvelope(text []byte) (envelope, []Issue) {
	e := envelope{text: text}
	doc, err := decodeJSON(text)
	if err != nil {
		return e, err.(*ValidationError).Issues
	}
	r := &reader{}
	o, ok := r.object("", doc)
	if !ok {
		return e, r.issues
	}
	e.v, _ = field(r, "", o, "v", required, r.integerFrom(1))
	e.typ, _ = field(r, "", o, "type", required, r.nonEmptyText)
	e.sender, _ = field(r, "", o, "sender", required, r.sender)
	e.room, _ = field(r, "", o, "room", optional, r.nonEmptyText)
	e.seq, e.hasSeq = field(r, "", o, "seq", optional, r.integerFrom(0))
	field(r, "", o, "event_id", optional, r.nonEmptyText)
	field(r, "", o, "ts", optional, r.nonEmptyText)
	if _, e.hasPayload = o["payload"]; e.hasPayload {
		var parts struct{ Payload json.RawMessage }
		_ = json.Unmarshal(text, &parts)
		e.payload = compact(parts.Payload)
	}
	return e, r.issues
}

func (r *reader) sender(path string, value any) Sender {
	var s Sender
	o, ok := r.object(path, value)
	if !ok {
		return s
	}
	s.ID, _ = field(r, path, o, "id", required, r.nonEmptyText)
	kind, _ := field(r, path, o, "kind", required, choice(r, string(Daemon), string(Dashboard), string(Server)))
	s.Kind = SenderKind(kind)
	return s
}

// event reads the payload as a stored event.
func (e envelope) event() (Event, []Issue) {
	if !e.hasPayload {
		return Event{}, []Issue{{Path: "payload", Reason: "is required"}}
	}
	ev, err := ParseEvent(e.payload)
	if err != nil {
		return Event{}, prefixed("payload", err.(*ValidationError).Issues)
	}
	return ev, nil
}

// readPayload reads the payload by type. With versionOne false only an event
// is read, which is all that the envelope schema asks for.
func (e envelope) readPayload(versionOne bool) (Payload, []Issue) {
	if !e.hasPayload {
		return nil, []Issue{{Path: "payload", Reason: "is required"}}
	}
	var payload Payload
	var err error
	switch {
	case e.typ == "event":
		ev, issues := e.event()
		return ev, issues
	case versionOne && e.typ == "resume":
		payload, err = parse(e.payload, (*reader).resume)
	case versionOne && e.typ == "error":
		payload, err = parse(e.payload, (*reader).errorPayload)
	default:
		return Other{Raw: e.payload}, nil
	}
	if err != nil {
		return nil, prefixed("payload", err.(*ValidationError).Issues)
	}
	return payload, nil
}

func prefixed(path string, issues []Issue) []Issue {
	out := make([]Issue, len(issues))
	for i, issue := range issues {
		out[i] = Issue{Path: path, Reason: issue.Reason}
		if issue.Path != "" {
			out[i].Path = member(path, issue.Path)
		}
	}
	return out
}
