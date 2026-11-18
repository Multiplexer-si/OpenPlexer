package room

// Subscription is one connection's subscription to one Room. It is in one of
// two states: replaying, where a live event waits behind the replay, and live.
// Each transition returns the events to send now, in order. An event at or
// below the last one sent is dropped, so the order on the connection never goes
// back and an event that arrives twice is sent once.
type Subscription struct {
	last int64
	live bool
	held []Event
}

// Subscribe starts a replaying subscription after since.
func Subscribe(since int64) *Subscription { return &Subscription{last: since} }

// Replay takes a stored event read after since. Once the subscription is live
// it acts as Live.
func (s *Subscription) Replay(ev Event) []Event { return s.send(ev) }

// Live takes a new event. It is held until GoLive, so it is not sent before an
// older event of the replay.
func (s *Subscription) Live(ev Event) []Event {
	if !s.live {
		s.held = append(s.held, ev)
		return nil
	}
	return s.send(ev)
}

// GoLive ends the replay and returns the held events that are still new. A
// second call returns nil.
func (s *Subscription) GoLive() []Event {
	if s.live {
		return nil
	}
	s.live = true
	var out []Event
	for _, ev := range s.held {
		out = append(out, s.send(ev)...)
	}
	s.held = nil
	return out
}

// LastSeq is the seq of the last event sent, or since if none was sent.
func (s *Subscription) LastSeq() int64 { return s.last }

func (s *Subscription) send(ev Event) []Event {
	if ev.seq <= s.last {
		return nil
	}
	s.last = ev.seq
	return []Event{ev}
}
