package policy

type Action string

const (
	Read    Action = "read"
	Write   Action = "write"
	Exec    Action = "exec"
	GitPush Action = "git.push"
)

type Bundle struct {
	Version         int64
	ProjectID       string
	Repos           []string
	Areas           []Area
	Roles           map[string]Role
	Members         map[string]string
	TemporaryGrants []TemporaryGrant
}

type Area struct {
	Name        string
	Patterns    []string
	Private     bool
	System      bool
	Generated   bool
	OwnerTeamID string
}

type Role struct {
	Read  []string
	Write []string
	Exec  []string
	Push  []string
}

type GrantScope string

const (
	GrantForMission GrantScope = "mission"
	GrantUntil      GrantScope = "until"
)

// TemporaryGrant uses MissionID under GrantForMission and ExpiresAt under
// GrantUntil.
type TemporaryGrant struct {
	MemberID  string
	Area      string
	Actions   []Action
	Scope     GrantScope
	MissionID string
	ExpiresAt Timestamp
}

type OverrideScope string

const (
	OverrideOnce       OverrideScope = "once"
	OverrideForMission OverrideScope = "mission"
	OverrideUntil      OverrideScope = "until"
)

// Override keeps ExpiresAt as text, because the schema accepts any string
// there. A value that is not a timestamp makes the override inactive.
type Override struct {
	ID             string
	Area           string
	Actions        []Action
	Scope          OverrideScope
	AgentSessionID *string
	MissionID      *string
	ExpiresAt      *string
}

// Query is one action to evaluate. Command is read only for Exec. A zero Now
// is no time, and every grant and override that expires is then inactive.
type Query struct {
	Repo           string
	Path           string
	Action         Action
	Member         string
	Command        string
	AgentSessionID *string
	MissionID      *string
	Parent         *Parent
	Overrides      []Override
	Now            Timestamp
}

// Parent is the session that started a sub-agent.
type Parent struct {
	AgentSessionID string
	MissionID      *string
}

type Decision string

const (
	Allow Decision = "allow"
	Deny  Decision = "deny"
)

// Result marshals to spec/policy/v1/eval-result.schema.json. Area is nil
// when no area decided. Rule is a sentence for a person, and its wording is
// not part of the contract.
type Result struct {
	Decision Decision `json:"decision"`
	Area     *string  `json:"area,omitempty"`
	Rule     string   `json:"rule"`
}
