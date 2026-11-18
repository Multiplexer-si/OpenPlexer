package room

// Body is a pointer to the typed body of one event type. A zero optional field
// is an absent one.
type Body interface {
	thread() *Thread
	read(r *reader, path string, o object)
}

type Thread struct{ ThreadID, ReplyTo string }

func (t *Thread) thread() *Thread { return t }

// eventTypes is the only place that names the event types of version 1.
var eventTypes = []struct {
	name string
	new  func() Body
}{
	{"status", func() Body { return new(Status) }},
	{"claim", func() Body { return new(Claim) }},
	{"release", func() Body { return new(Release) }},
	{"decision", func() Body { return new(Decision) }},
	{"decision.confirm", func() Body { return new(DecisionConfirm) }},
	{"question", func() Body { return new(Question) }},
	{"answer", func() Body { return new(Answer) }},
	{"blocked", func() Body { return new(Blocked) }},
	{"human", func() Body { return new(Human) }},
	{"policy.denied", func() Body { return new(PolicyDenied) }},
	{"policy.override", func() Body { return new(PolicyOverride) }},
	{"policy.override.revoked", func() Body { return new(PolicyOverrideRevoked) }},
}

func eventTypeNames() []string {
	names := make([]string, len(eventTypes))
	for i, t := range eventTypes {
		names[i] = t.name
	}
	return names
}

func newBody(typ string) Body {
	for _, t := range eventTypes {
		if t.name == typ {
			return t.new()
		}
	}
	return nil
}

func readBody(r *reader, path string, b Body, o object) {
	t := b.thread()
	t.ThreadID, _ = field(r, path, o, "thread_id", optional, r.text)
	t.ReplyTo, _ = field(r, path, o, "reply_to", optional, r.text)
	b.read(r, path, o)
}

type Status struct {
	Thread
	Summary, PlanStep string
	Files             []string
}

type Claim struct {
	Thread
	// ClaimID is the id that the hub gave the claim, or "". The body schema
	// does not name the field, so a value that is not a string is ignored.
	ClaimID      string
	Item, Intent string
	Paths        []string
	TTLSeconds   int64
}

type Release struct {
	Thread
	ClaimID, Outcome string
}

type Decision struct {
	Thread
	Title, Detail, Scope, Supersedes string
}

type DecisionConfirm struct {
	Thread
	DecisionID string
	Result     string
}

type Question struct {
	Thread
	Text, AddressedTo string
	AckRequired       bool
}

type Answer struct {
	Thread
	QuestionID, Text string
}

type Blocked struct {
	Thread
	Reason string
	Needs  *Needs
}

type Needs struct{ Decision, RequestID string }

type Human struct {
	Thread
	Text     string
	Mentions []string
}

type PolicyDenied struct {
	Thread
	Action, Path, Rule, Detected string
}

type PolicyOverride struct {
	Thread
	Lead, Agent, Scope, Reason string
	Areas, Actions             []string
}

type PolicyOverrideRevoked struct {
	Thread
	OverrideID, Agent, Area, RevokedBy, Reason string
}

func (b *Status) read(r *reader, path string, o object) {
	b.Summary, _ = field(r, path, o, "summary", required, r.text)
	b.PlanStep, _ = field(r, path, o, "plan_step", optional, r.text)
	b.Files, _ = field(r, path, o, "files", optional, list(r, r.text))
}

func (b *Claim) read(r *reader, path string, o object) {
	b.Item, _ = field(r, path, o, "item", required, r.text)
	b.Paths, _ = field(r, path, o, "paths", required, list(r, r.text))
	b.Intent, _ = field(r, path, o, "intent", required, r.text)
	b.TTLSeconds, _ = field(r, path, o, "ttl_seconds", required, r.integerFrom(1))
	b.ClaimID, _ = o["claim_id"].(string)
}

func (b *Release) read(r *reader, path string, o object) {
	b.ClaimID, _ = field(r, path, o, "claim_id", required, r.text)
	b.Outcome, _ = field(r, path, o, "outcome", optional, r.text)
}

func (b *Decision) read(r *reader, path string, o object) {
	b.Title, _ = field(r, path, o, "title", required, r.text)
	b.Detail, _ = field(r, path, o, "detail", required, r.text)
	b.Scope, _ = field(r, path, o, "scope", optional, r.text)
	b.Supersedes, _ = field(r, path, o, "supersedes", optional, r.text)
}

func (b *DecisionConfirm) read(r *reader, path string, o object) {
	b.DecisionID, _ = field(r, path, o, "decision_id", required, r.text)
	b.Result, _ = field(r, path, o, "result", required, choice(r, "confirmed", "rejected"))
}

func (b *Question) read(r *reader, path string, o object) {
	b.Text, _ = field(r, path, o, "text", required, r.nonBlankText)
	b.AddressedTo, _ = field(r, path, o, "addressed_to", optional, r.nonBlankText)
	b.AckRequired, _ = field(r, path, o, "ack_required", optional, r.flag)
}

func (b *Answer) read(r *reader, path string, o object) {
	b.QuestionID, _ = field(r, path, o, "question_id", required, r.nonEmptyText)
	b.Text, _ = field(r, path, o, "text", required, r.nonBlankText)
}

func (b *Blocked) read(r *reader, path string, o object) {
	b.Reason, _ = field(r, path, o, "reason", required, r.text)
	if needs, present := field(r, path, o, "needs", optional, r.needs); present {
		b.Needs = &needs
	}
}

func (r *reader) needs(path string, value any) Needs {
	var n Needs
	o, ok := r.object(path, value)
	if !ok {
		return n
	}
	n.Decision, _ = field(r, path, o, "decision", optional, r.text)
	n.RequestID, _ = field(r, path, o, "request_id", optional, r.text)
	return n
}

func (b *Human) read(r *reader, path string, o object) {
	b.Text, _ = field(r, path, o, "text", required, r.text)
	b.Mentions, _ = field(r, path, o, "mentions", optional, list(r, r.text))
}

func (b *PolicyDenied) read(r *reader, path string, o object) {
	b.Action, _ = field(r, path, o, "action", required, r.text)
	b.Path, _ = field(r, path, o, "path", required, r.text)
	b.Rule, _ = field(r, path, o, "rule", required, r.text)
	b.Detected, _ = field(r, path, o, "detected", optional, r.text)
}

func (b *PolicyOverride) read(r *reader, path string, o object) {
	b.Lead, _ = field(r, path, o, "lead", required, r.text)
	b.Agent, _ = field(r, path, o, "agent", required, r.text)
	b.Areas, _ = field(r, path, o, "areas", required, list(r, r.text))
	b.Actions, _ = field(r, path, o, "actions", required, list(r, r.text))
	b.Scope, _ = field(r, path, o, "scope", required, r.text)
	b.Reason, _ = field(r, path, o, "reason", required, r.text)
}

func (b *PolicyOverrideRevoked) read(r *reader, path string, o object) {
	b.OverrideID, _ = field(r, path, o, "override_id", required, r.text)
	b.Agent, _ = field(r, path, o, "agent", required, r.text)
	b.Area, _ = field(r, path, o, "area", required, r.text)
	b.RevokedBy, _ = field(r, path, o, "revoked_by", required, r.text)
	b.Reason, _ = field(r, path, o, "reason", optional, r.text)
}
