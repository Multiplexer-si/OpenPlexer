package room

import (
	"slices"
	"time"
)

// ActiveClaim is a claim that a Receiver holds. Holder is the agent of the
// claim event.
type ActiveClaim struct {
	ID        string
	Intent    string
	Paths     []string
	ExpiresAt time.Time
	Holder    Agent
}

// reduceClaim applies one event to the claims of a Room. Only claim and
// release events change them. An expired claim stays until it is replaced:
// no event ends it, and active filters it out.
func reduceClaim(claims []ActiveClaim, ev Event) []ActiveClaim {
	switch body := ev.Body().(type) {
	case *Claim:
		at, err := time.Parse(time.RFC3339, ev.at)
		if err != nil {
			return claims
		}
		id, ok := ev.body["claim_id"].(string)
		if !ok {
			id = ev.id
		}
		expires := at.UnixMilli() + body.TTLSeconds*1000
		claims = slices.DeleteFunc(claims, func(c ActiveClaim) bool { return c.ID == id })
		return append(claims, ActiveClaim{
			ID: id, Intent: body.Intent, Paths: body.Paths,
			ExpiresAt: time.UnixMilli(expires).UTC(), Holder: ev.agent,
		})
	case *Release:
		return slices.DeleteFunc(claims, func(c ActiveClaim) bool {
			return c.ID == body.ClaimID && c.Holder.ID == ev.agent.ID
		})
	}
	return claims
}

func active(claims []ActiveClaim, now time.Time) []ActiveClaim {
	out := []ActiveClaim{}
	for _, c := range claims {
		if c.ExpiresAt.UnixMilli() > now.UnixMilli() {
			c.Paths = slices.Clone(c.Paths)
			out = append(out, c)
		}
	}
	return out
}
