package room

import (
	"slices"
	"time"
)

// Receiver is the receiving end of a connection, kept across reconnections.
// Per Room it holds the highest seq received and the claims reduced from the
// events that Receive delivered.
type Receiver struct {
	rooms map[string]*roomState
}

type roomState struct {
	last   int64
	claims []ActiveClaim
}

// Step is what to do with one frame: Deliver, SendResume, Skipped, or
// Unhandled.
type Step interface{ step() }

// Deliver is a new event. It is already counted and reduced into the claims.
type Deliver struct {
	Room  string
	Event Event
}

// SendResume is a resume to send now, the answer to `behind`.
type SendResume struct {
	Room    string
	LastSeq int64
}

// Skipped needs nothing. Reason is for a log.
type Skipped struct{ Reason string }

// Unhandled is a valid message that no rule here covers. The caller decides.
type Unhandled struct{ Message Message }

func (Deliver) step()    {}
func (SendResume) step() {}
func (Skipped) step()    {}
func (Unhandled) step()  {}

// undecided names the messages that spec/room/README.md leaves out of version
// 1 until the operator decides.
var undecided = []string{"digest", "digest.get", "receipt"}

func NewReceiver() *Receiver { return &Receiver{rooms: map[string]*roomState{}} }

func (r *Receiver) room(name string) *roomState {
	state := r.rooms[name]
	if state == nil {
		state = &roomState{}
		r.rooms[name] = state
	}
	return state
}

// Restore sets the highest seq of a Room from durable storage. It never lowers
// the seq.
func (r *Receiver) Restore(room string, lastSeq int64) {
	if state := r.room(room); lastSeq > state.last {
		state.last = lastSeq
	}
}

// LastSeq is the highest seq received for room, 0 for a Room with no event. It
// is the last_seq of a resume.
func (r *Receiver) LastSeq(room string) int64 {
	if state := r.rooms[room]; state != nil {
		return state.last
	}
	return 0
}

// Claims lists the claims of room that are active at now, oldest first.
func (r *Receiver) Claims(room string, now time.Time) []ActiveClaim {
	if state := r.rooms[room]; state != nil {
		return active(state.claims, now)
	}
	return []ActiveClaim{}
}

// Receive reads one frame from the hub, which arrived at now. A frame that is
// not a valid message of version 1 is skipped and sends no error. An event
// that this version cannot read is counted as received when its envelope names
// a Room and a seq, so a resume does not ask for it again. A claim whose at is
// not a timestamp of RFC 3339 expires ttl_seconds after now.
func (r *Receiver) Receive(frame []byte, now time.Time) Step {
	env, issues := readEnvelope(frame)
	if len(issues) > 0 {
		return Skipped{Reason: "not a valid envelope: " + issues[0].String()}
	}
	if env.v != 1 {
		return Skipped{Reason: "unsupported version"}
	}
	if env.typ == "event" {
		return r.receiveEvent(env, now)
	}
	if slices.Contains(undecided, env.typ) {
		return Skipped{Reason: "version 1 defines no " + env.typ + " message"}
	}
	msg, err := ParseMessage(frame)
	if err != nil {
		return Skipped{Reason: "not a valid message: " + err.Error()}
	}
	if payload, ok := msg.Payload().(Error); ok && errorCodes[payload.Code].resumes && msg.Room() != "" {
		return SendResume{Room: msg.Room(), LastSeq: r.LastSeq(msg.Room())}
	}
	return Unhandled{Message: msg}
}

func (r *Receiver) receiveEvent(env envelope, now time.Time) Step {
	ev, issues := env.event()
	if len(issues) > 0 {
		if env.room != "" && env.hasSeq {
			r.Restore(env.room, env.seq)
		}
		return Skipped{Reason: "not a valid event: " + issues[0].String()}
	}
	room := env.room
	if room == "" {
		room = ev.mission
	}
	state := r.room(room)
	if ev.seq <= state.last {
		return Skipped{Reason: "duplicate"}
	}
	state.last = ev.seq
	state.claims = reduceClaim(state.claims, ev, now)
	return Deliver{Room: room, Event: ev}
}
