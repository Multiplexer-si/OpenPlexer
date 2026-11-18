package room

import "encoding/json"

// Agent names who posted an event. Parent is "" unless the event is from a
// sub-agent.
type Agent struct{ ID, Vendor, Parent string }

// Event is a stored event that passed room-event.schema.json. It is immutable.
// It keeps the text it was read from, so unknown fields, key order, and the
// spelling of numbers survive MarshalJSON.
type Event struct {
	raw        json.RawMessage
	body       object
	id         string
	seq        int64
	mission    string
	typ        string
	agent      Agent
	onBehalfOf string
	at         string
}

// ParseEvent reads a stored event and validates it against
// spec/room/v1/room-event.schema.json. The error is a *ValidationError.
func ParseEvent(text []byte) (Event, error) {
	ev, err := parse(text, (*reader).event)
	if err != nil {
		return Event{}, err
	}
	ev.raw = compact(text)
	return ev, nil
}

func (e Event) ID() string         { return e.id }
func (e Event) Seq() int64         { return e.seq }
func (e Event) Mission() string    { return e.mission }
func (e Event) Type() string       { return e.typ }
func (e Event) Agent() Agent       { return e.agent }
func (e Event) OnBehalfOf() string { return e.onBehalfOf }
func (e Event) At() string         { return e.at }

// Body reads the body again on each call, so a caller that changes the result
// changes nothing else. The zero Event has no body.
func (e Event) Body() Body {
	b := newBody(e.typ)
	if b == nil {
		return nil
	}
	readBody(&reader{}, "body", b, e.body)
	return b
}

func (e Event) MarshalJSON() ([]byte, error) {
	if e.raw == nil {
		return nil, &ValidationError{Issues: []Issue{{Reason: "is the zero Event"}}}
	}
	return e.raw, nil
}

func (r *reader) event(doc any) Event {
	var e Event
	o, ok := r.object("", doc)
	if !ok {
		return e
	}
	e.id, _ = field(r, "", o, "id", required, r.nonEmptyText)
	e.seq, _ = field(r, "", o, "seq", required, r.integerFrom(0))
	e.mission, _ = field(r, "", o, "mission", required, r.nonEmptyText)
	e.typ, _ = field(r, "", o, "type", required, r.eventType)
	e.agent, _ = field(r, "", o, "agent", required, r.agent)
	e.onBehalfOf, _ = field(r, "", o, "on_behalf_of", required, r.nonEmptyText)
	e.at, _ = field(r, "", o, "at", required, r.nonEmptyText)
	e.body = r.eventBody(o, e.typ)
	return e
}

func (r *reader) eventType(path string, value any) string {
	return choice(r, eventTypeNames()...)(path, value)
}

func (r *reader) eventBody(o object, kind string) object {
	value, present := o["body"]
	if !present {
		r.fail("body", "is required")
		return nil
	}
	if b := newBody(kind); b != nil {
		if body, ok := r.object("body", value); ok {
			readBody(r, "body", b, body)
			return body
		}
	}
	return nil
}

func (r *reader) agent(path string, value any) Agent {
	var a Agent
	o, ok := r.object(path, value)
	if !ok {
		return a
	}
	a.ID, _ = field(r, path, o, "id", required, r.nonEmptyText)
	a.Vendor, _ = field(r, path, o, "vendor", required, r.nonEmptyText)
	a.Parent, _ = field(r, path, o, "parent", optional, r.nonEmptyText)
	return a
}
