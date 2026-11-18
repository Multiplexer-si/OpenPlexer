package room

import (
	"reflect"
	"strconv"
	"testing"
	"time"
)

const (
	sender      = `"sender":{"id":"ses","kind":"daemon"}`
	statusAt    = `"at":"2026-01-15T12:00:00.000Z"`
	statusAgent = `"agent":{"id":"ses","vendor":"v"},"on_behalf_of":"m"`
)

func eventPayload(typ string, seq int, body string) string {
	return `{"id":"e` + strconv.Itoa(seq) + `","seq":` + strconv.Itoa(seq) + `,"mission":"mis","type":"` + typ + `",` + statusAgent + `,` + statusAt + `,"body":` + body + `}`
}

func eventFrame(room string, seq int, payload string) string {
	frame := `{"v":1,"type":"event",` + sender + `,"payload":` + payload
	if room != "" {
		frame += `,"room":"` + room + `"`
	}
	return frame + `,"seq":` + strconv.Itoa(seq) + `}`
}

func statusFrame(room string, seq int) string {
	return eventFrame(room, seq, eventPayload("status", seq, `{"summary":"s"}`))
}

func TestReceive(t *testing.T) {
	deliver := func(room string, seq int) Step {
		return Deliver{Room: room, Event: mustEvent(t, eventPayload("status", seq, `{"summary":"s"}`))}
	}
	for _, c := range []struct {
		name   string
		frames []string
		want   Step
		room   string
		last   int64
	}{
		{"text that is not JSON", []string{`nope`}, Skipped{}, "mis", 0},
		{"JSON that is not an object", []string{`[1]`}, Skipped{}, "mis", 0},
		{"an event envelope with no sender", []string{`{"v":1,"type":"event","room":"mis","seq":4,"payload":` + eventPayload("status", 4, `{"summary":"s"}`) + `}`}, Skipped{}, "mis", 0},
		{"an event of another version", []string{`{"v":2,"type":"event",` + sender + `,"room":"mis","seq":4,"payload":` + eventPayload("status", 4, `{"summary":"s"}`) + `}`}, Skipped{}, "mis", 0},
		{"a new event", []string{statusFrame("mis", 3)}, deliver("mis", 3), "mis", 3},
		{"an event with no room in the envelope", []string{statusFrame("", 3)}, deliver("mis", 3), "mis", 3},
		{"an event for the envelope room", []string{statusFrame("other", 3)}, Deliver{Room: "other", Event: mustEvent(t, eventPayload("status", 3, `{"summary":"s"}`))}, "other", 3},
		{"the same event twice", []string{statusFrame("mis", 3), statusFrame("mis", 3)}, Skipped{}, "mis", 3},
		{"an older event", []string{statusFrame("mis", 3), statusFrame("mis", 2)}, Skipped{}, "mis", 3},
		{"a gap", []string{statusFrame("mis", 3), statusFrame("mis", 9)}, deliver("mis", 9), "mis", 9},
		{"the seq of the event, not of the envelope", []string{`{"v":1,"type":"event",` + sender + `,"room":"mis","seq":50,"payload":` + eventPayload("status", 3, `{"summary":"s"}`) + `}`}, deliver("mis", 3), "mis", 3},
		{"an event of a type that v1 does not define", []string{eventFrame("mis", 6, eventPayload("request", 6, `{}`))}, Skipped{}, "mis", 6},
		{"an event that is not valid, below the last seq", []string{statusFrame("mis", 8), eventFrame("mis", 6, eventPayload("request", 6, `{}`))}, Skipped{}, "mis", 8},
		{"an event that is not valid and has no room", []string{`{"v":1,"type":"event",` + sender + `,"seq":6,"payload":{}}`}, Skipped{}, "mis", 0},
		{"an event that is not valid and has no seq", []string{`{"v":1,"type":"event",` + sender + `,"room":"mis","payload":null}`}, Skipped{}, "mis", 0},
		{"an event with no payload", []string{`{"v":1,"type":"event",` + sender + `,"room":"mis","seq":6}`}, Skipped{}, "mis", 6},
		{"a resume whose payload is not valid", []string{`{"v":1,"type":"resume",` + sender + `,"payload":{"last_seq":-1}}`}, Skipped{}, "mis", 0},
		{"a message of another version", []string{`{"v":2,"type":"error",` + sender + `,"room":"mis","payload":{"code":"behind","message":"x"}}`}, Skipped{}, "mis", 0},
		{"behind with a room", []string{statusFrame("mis", 5), `{"v":1,"type":"error",` + sender + `,"room":"mis","payload":{"code":"behind","message":"x"}}`}, SendResume{Room: "mis", LastSeq: 5}, "mis", 5},
		{"behind for a Room with no event", []string{`{"v":1,"type":"error",` + sender + `,"room":"new","payload":{"code":"behind","message":"x"}}`}, SendResume{Room: "new", LastSeq: 0}, "new", 0},
		{"behind with no room", []string{`{"v":1,"type":"error",` + sender + `,"payload":{"code":"behind","message":"x"}}`}, nil, "mis", 0},
		{"another error with a room", []string{`{"v":1,"type":"error",` + sender + `,"room":"mis","payload":{"code":"rate_limit","message":"x"}}`}, nil, "mis", 0},
		{"a type that v1 does not define", []string{`{"v":1,"type":"hello",` + sender + `,"payload":{"k":1}}`}, nil, "mis", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := NewReceiver()
			var got Step
			for _, frame := range c.frames {
				got = r.Receive([]byte(frame))
			}
			switch want := c.want.(type) {
			case nil:
				unhandled, ok := got.(Unhandled)
				if !ok || unhandled.Message.Type() == "" {
					t.Errorf("step = %+v, want Unhandled with a message", got)
				}
			case Skipped:
				if skipped, ok := got.(Skipped); !ok || skipped.Reason == "" {
					t.Errorf("step = %+v, want Skipped with a reason", got)
				}
			default:
				if !reflect.DeepEqual(got, want) {
					t.Errorf("step = %+v, want %+v", got, want)
				}
			}
			if last := r.LastSeq(c.room); last != c.last {
				t.Errorf("LastSeq(%s) = %d, want %d", c.room, last, c.last)
			}
		})
	}
}

func TestReceiveKeepsRoomsApart(t *testing.T) {
	r := NewReceiver()
	r.Receive([]byte(statusFrame("a", 4)))
	if _, ok := r.Receive([]byte(statusFrame("b", 2))).(Deliver); !ok {
		t.Error("an event of room b was dropped by the seq of room a")
	}
	if r.LastSeq("a") != 4 || r.LastSeq("b") != 2 {
		t.Errorf("LastSeq = %d, %d", r.LastSeq("a"), r.LastSeq("b"))
	}
}

func TestRestoreThenDuplicate(t *testing.T) {
	r := NewReceiver()
	r.Restore("mis", 5)
	if _, ok := r.Receive([]byte(statusFrame("mis", 5))).(Skipped); !ok {
		t.Error("an event at the restored seq was delivered")
	}
	if _, ok := r.Receive([]byte(statusFrame("mis", 6))).(Deliver); !ok {
		t.Error("an event after the restored seq was dropped")
	}
	r.Restore("mis", 2)
	if r.LastSeq("mis") != 6 {
		t.Errorf("Restore lowered the seq to %d", r.LastSeq("mis"))
	}
}

func TestReceiveReducesClaims(t *testing.T) {
	claim := func(seq int, id string, ttl string) string {
		return eventFrame("mis", seq, eventPayload("claim", seq, `{"claim_id":"`+id+`","item":"i","paths":["a/**"],"intent":"x","ttl_seconds":`+ttl+`}`))
	}
	r := NewReceiver()
	r.Receive([]byte(claim(1, "c1", "60")))
	r.Receive([]byte(claim(1, "c-duplicate", "60")))
	r.Receive([]byte(claim(2, "c2", "10")))
	at := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	if got := r.Claims("mis", at.Add(5*time.Second)); len(got) != 2 || got[0].ID != "c1" || got[1].ID != "c2" {
		t.Errorf("claims = %+v, want c1 and c2", got)
	}
	if got := r.Claims("mis", at.Add(10*time.Second)); len(got) != 1 || got[0].ID != "c1" {
		t.Errorf("claims after c2 expired = %+v, want c1", got)
	}
	if got := r.Claims("none", at); got == nil || len(got) != 0 {
		t.Errorf("claims of an unknown room = %#v, want an empty list", got)
	}
	got := r.Claims("mis", at)
	got[0].Paths[0] = "changed"
	if r.Claims("mis", at)[0].Paths[0] != "a/**" {
		t.Error("a change to a returned claim reached the Receiver")
	}
}
