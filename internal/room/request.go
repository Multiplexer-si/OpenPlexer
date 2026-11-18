package room

// EventPost holds the client-supplied fields of an event. The hub assigns
// the agent for a human post and supplies the event metadata.
type EventPost struct {
	Type  string
	Agent *Agent
	Body  map[string]any
}

func ParseEventPost(text []byte) (EventPost, error) {
	return parse(text, func(r *reader, doc any) EventPost {
		var post EventPost
		if o, ok := r.object("", doc); ok {
			post.Type, _ = field(r, "", o, "type", required, r.eventType)
			if agent, present := field(r, "", o, "agent", optional, r.agent); present {
				post.Agent = &agent
			}
			post.Body = r.eventBody(o, post.Type)
		}
		return post
	})
}

// ClaimPost holds the client's claim fields. The hub assigns a TTL when
// TTLSeconds is zero, which means the client omitted it.
type ClaimPost struct {
	Agent      Agent
	Item       string
	Paths      []string
	Intent     string
	Mode       string
	Implicit   bool
	TTLSeconds int64
}

func ParseClaimPost(text []byte) (ClaimPost, error) {
	return parse(text, func(r *reader, doc any) ClaimPost {
		var post ClaimPost
		if o, ok := r.object("", doc); ok {
			post.Agent, _ = field(r, "", o, "agent", required, r.agent)
			post.Item, _ = field(r, "", o, "item", required, r.text)
			post.Paths, _ = field(r, "", o, "paths", required, list(r, r.text))
			post.Intent, _ = field(r, "", o, "intent", required, r.text)
			post.Mode, _ = field(r, "", o, "mode", optional, r.text)
			post.Implicit, _ = field(r, "", o, "implicit", optional, r.flag)
			post.TTLSeconds, _ = field(r, "", o, "ttl_seconds", optional, r.integerFrom(1))
		}
		return post
	})
}

type ClaimRenewal struct {
	SessionID  string
	TTLSeconds int64
}

func ParseClaimRenewal(text []byte) (ClaimRenewal, error) {
	return parse(text, func(r *reader, doc any) ClaimRenewal {
		var renewal ClaimRenewal
		if o, ok := r.object("", doc); ok {
			renewal.SessionID, _ = field(r, "", o, "session_id", required, r.nonEmptyText)
			renewal.TTLSeconds, _ = field(r, "", o, "ttl_seconds", required, r.integerFrom(1))
		}
		return renewal
	})
}

type ClaimRelease struct {
	SessionID string
	Outcome   string
}

func ParseClaimRelease(text []byte) (ClaimRelease, error) {
	return parse(text, func(r *reader, doc any) ClaimRelease {
		var release ClaimRelease
		if o, ok := r.object("", doc); ok {
			release.SessionID, _ = field(r, "", o, "session_id", required, r.nonEmptyText)
			release.Outcome, _ = field(r, "", o, "outcome", optional, r.text)
		}
		return release
	})
}
