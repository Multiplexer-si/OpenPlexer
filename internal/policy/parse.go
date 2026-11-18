package policy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Issue is one reason why a document is not valid. Path names the place in
// the document, such as areas[2].patterns[0], and is empty for the document
// itself.
type Issue struct {
	Path   string
	Reason string
}

func (i Issue) String() string {
	if i.Path == "" {
		return i.Reason
	}
	return i.Path + " " + i.Reason
}

// ValidationError holds every reason why a document was rejected. It has at
// least one issue.
type ValidationError struct {
	Issues []Issue
}

func (e *ValidationError) Error() string {
	reasons := make([]string, len(e.Issues))
	for i, issue := range e.Issues {
		reasons[i] = issue.String()
	}
	return strings.Join(reasons, "; ")
}

// ParseBundle reads a policy bundle from JSON text and validates it against
// spec/policy/v1/bundle.schema.json. The error is a *ValidationError.
func ParseBundle(text []byte) (*Bundle, error) {
	return parse(text, func(r *reader, doc any) *Bundle {
		bundle := r.bundle(doc)
		return &bundle
	})
}

// ParseOverride reads an override from JSON text and validates it against
// spec/policy/v1/override.schema.json. The error is a *ValidationError.
func ParseOverride(text []byte) (Override, error) {
	return parse(text, func(r *reader, doc any) Override { return r.override("", doc) })
}

// ParseQuery reads an evaluation query from JSON text and validates it
// against spec/policy/v1/eval-query.schema.json. The error is a
// *ValidationError.
func ParseQuery(text []byte) (Query, error) {
	return parse(text, (*reader).query)
}

func parse[T any](text []byte, read func(*reader, any) T) (T, error) {
	var none T
	decoder := json.NewDecoder(bytes.NewReader(text))
	decoder.UseNumber()
	// The document is decoded into a plain value, not into a struct. For a
	// repeated key the last member then replaces the earlier ones, and a key
	// matches a field name only when the two are equal.
	var doc any
	if err := decoder.Decode(&doc); err != nil {
		return none, &ValidationError{Issues: []Issue{{Reason: "is not valid JSON"}}}
	}
	if _, err := decoder.Token(); err != io.EOF {
		return none, &ValidationError{Issues: []Issue{{Reason: "has text after the JSON value"}}}
	}
	if hasLoneSurrogate(text[:decoder.InputOffset()]) {
		return none, &ValidationError{Issues: []Issue{{Reason: `holds an escape of a lone surrogate, such as \ud800, which is not a character`}}}
	}
	r := &reader{}
	value := read(r, doc)
	if len(r.issues) > 0 {
		return none, &ValidationError{Issues: r.issues}
	}
	return value, nil
}

// hasLoneSurrogate reports whether JSON text holds a \u escape of a
// surrogate that is not half of a pair. encoding/json reads such an escape
// as U+FFFD, so a pattern or a path would not be the text of the document.
func hasLoneSurrogate(text []byte) bool {
	for i := 0; i < len(text); i++ {
		if text[i] != '\\' {
			continue
		}
		unit, isUnit := escapedUnit(text[i:])
		if !isUnit {
			i++
			continue
		}
		i += 5
		if !utf16.IsSurrogate(unit) {
			continue
		}
		low, _ := escapedUnit(text[i+1:])
		if utf16.DecodeRune(unit, low) == '\uFFFD' {
			return true
		}
		i += 6
	}
	return false
}

func escapedUnit(text []byte) (rune, bool) {
	if len(text) < 6 || text[0] != '\\' || text[1] != 'u' {
		return 0, false
	}
	unit, err := strconv.ParseUint(string(text[2:6]), 16, 16)
	return rune(unit), err == nil
}

type reader struct {
	issues []Issue
}

func (r *reader) fail(path, reason string) {
	r.issues = append(r.issues, Issue{Path: path, Reason: reason})
}

func member(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

const (
	required = true
	optional = false
)

func field[T any](r *reader, path string, object map[string]any, key string, need bool, read func(path string, value any) T) (T, bool) {
	value, present := object[key]
	if !present {
		if need {
			r.fail(member(path, key), "is required")
		}
		var none T
		return none, false
	}
	return read(member(path, key), value), true
}

func optionalText(r *reader, path string, object map[string]any, key string) *string {
	if text, present := field(r, path, object, key, optional, r.text); present {
		return &text
	}
	return nil
}

func (r *reader) object(path string, value any) (map[string]any, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		r.fail(path, "must be an object")
	}
	return object, ok
}

func (r *reader) rejectUnknownFields(path string, object map[string]any, allowed ...string) {
	for key := range object {
		if !slices.Contains(allowed, key) {
			r.fail(path, "has an unknown field")
			return
		}
	}
}

func (r *reader) text(path string, value any) string {
	text, ok := value.(string)
	if !ok {
		r.fail(path, "must be a string")
	}
	return text
}

func (r *reader) nonEmptyText(path string, value any) string {
	text, ok := value.(string)
	if !ok || text == "" {
		r.fail(path, "must be a string that is not empty")
	}
	return text
}

func (r *reader) flag(path string, value any) bool {
	flag, ok := value.(bool)
	if !ok {
		r.fail(path, "must be true or false")
	}
	return flag
}

const maxSafeInteger = 1<<53 - 1

// integer reads the number as an IEEE 754 double, as spec/README.md says, so
// 1.0000000000000001 is the integer 1 and 1e400 is infinity.
func (r *reader) integer(path string, value any) int64 {
	number, ok := value.(json.Number)
	if ok {
		double, _ := strconv.ParseFloat(string(number), 64)
		if double == math.Trunc(double) && math.Abs(double) <= maxSafeInteger {
			return int64(double)
		}
	}
	r.fail(path, fmt.Sprintf("must be an integer from -%d to %d", maxSafeInteger, maxSafeInteger))
	return 0
}

func (r *reader) timestamp(path string, value any) Timestamp {
	text, _ := value.(string)
	instant, ok := ParseTimestamp(text)
	if !ok {
		r.fail(path, "must be a timestamp such as 2026-01-15T12:00:00Z or 2026-01-15T12:00:00.250+01:00")
	}
	return instant
}

func choice[T ~string](r *reader, allowed ...T) func(path string, value any) T {
	return func(path string, value any) T {
		text, ok := value.(string)
		if !ok || !slices.Contains(allowed, T(text)) {
			quoted := make([]string, len(allowed))
			for i, option := range allowed {
				quoted[i] = strconv.Quote(string(option))
			}
			r.fail(path, "must be one of "+strings.Join(quoted, ", "))
		}
		return T(text)
	}
}

func list[T any](r *reader, read func(path string, value any) T) func(path string, value any) []T {
	return func(path string, value any) []T {
		elements, ok := value.([]any)
		if !ok {
			r.fail(path, "must be a list")
			return nil
		}
		out := make([]T, len(elements))
		for i, element := range elements {
			out[i] = read(fmt.Sprintf("%s[%d]", path, i), element)
		}
		return out
	}
}

func dictionary[T any](r *reader, read func(path string, value any) T) func(path string, value any) map[string]T {
	return func(path string, value any) map[string]T {
		object, _ := r.object(path, value)
		out := make(map[string]T, len(object))
		for _, key := range slices.Sorted(maps.Keys(object)) {
			out[key] = read(path+"[*]", object[key])
		}
		return out
	}
}

func (r *reader) bundle(doc any) Bundle {
	var b Bundle
	object, ok := r.object("", doc)
	if !ok {
		return b
	}
	b.Version, _ = field(r, "", object, "version", required, r.integer)
	b.ProjectID, _ = field(r, "", object, "project_id", required, r.text)
	b.Repos, _ = field(r, "", object, "repos", required, list(r, r.text))
	b.Areas, _ = field(r, "", object, "areas", required, list(r, r.area))
	b.Roles, _ = field(r, "", object, "roles", required, dictionary(r, r.role))
	b.Members, _ = field(r, "", object, "members", required, dictionary(r, r.text))
	b.TemporaryGrants, _ = field(r, "", object, "temporary_grants", optional, list(r, r.temporaryGrant))
	if len(b.TemporaryGrants) > maxTemporaryGrants {
		r.fail("temporary_grants", fmt.Sprintf("must hold at most %d grants", maxTemporaryGrants))
	}
	return b
}

const maxTemporaryGrants = 200

func (r *reader) area(path string, value any) Area {
	var a Area
	object, ok := r.object(path, value)
	if !ok {
		return a
	}
	a.Name, _ = field(r, path, object, "name", required, r.text)
	a.Patterns, _ = field(r, path, object, "patterns", required, list(r, r.text))
	a.Private, _ = field(r, path, object, "private", optional, r.flag)
	a.System, _ = field(r, path, object, "system", optional, r.flag)
	a.Generated, _ = field(r, path, object, "generated", optional, r.flag)
	a.OwnerTeamID, _ = field(r, path, object, "owner_team_id", optional, r.text)
	return a
}

func (r *reader) role(path string, value any) Role {
	var role Role
	object, ok := r.object(path, value)
	if !ok {
		return role
	}
	role.Read, _ = field(r, path, object, "read", required, list(r, r.text))
	role.Write, _ = field(r, path, object, "write", required, list(r, r.text))
	role.Exec, _ = field(r, path, object, "exec", required, list(r, r.text))
	role.Push, _ = field(r, path, object, "push", optional, list(r, r.text))
	return role
}

func (r *reader) temporaryGrant(path string, value any) TemporaryGrant {
	var g TemporaryGrant
	object, ok := r.object(path, value)
	if !ok {
		return g
	}
	g.MemberID, _ = field(r, path, object, "member_id", required, r.nonEmptyText)
	g.Area, _ = field(r, path, object, "area", required, r.nonEmptyText)
	g.Actions, _ = field(r, path, object, "actions", required, list(r, choice(r, Read, Write)))
	if actions, ok := object["actions"].([]any); ok && (len(actions) < 1 || len(actions) > 2) {
		r.fail(member(path, "actions"), "must hold one or two actions")
	}
	g.Scope, _ = field(r, path, object, "scope", required, choice(r, GrantForMission, GrantUntil))
	fields := []string{"member_id", "area", "actions", "scope"}
	switch g.Scope {
	case GrantForMission:
		g.MissionID, _ = field(r, path, object, "mission_id", required, r.nonEmptyText)
		fields = append(fields, "mission_id")
	case GrantUntil:
		g.ExpiresAt, _ = field(r, path, object, "expires_at", required, r.timestamp)
		fields = append(fields, "expires_at")
	default:
		fields = append(fields, "mission_id", "expires_at")
	}
	r.rejectUnknownFields(path, object, fields...)
	return g
}

func (r *reader) override(path string, value any) Override {
	var o Override
	object, ok := r.object(path, value)
	if !ok {
		return o
	}
	o.ID, _ = field(r, path, object, "id", required, r.text)
	o.Area, _ = field(r, path, object, "area", required, r.text)
	o.Actions, _ = field(r, path, object, "actions", required, list(r, choice(r, Read, Write, Exec, GitPush)))
	o.Scope, _ = field(r, path, object, "scope", required, choice(r, OverrideOnce, OverrideForMission, OverrideUntil))
	o.AgentSessionID = optionalText(r, path, object, "agent_session_id")
	o.MissionID = optionalText(r, path, object, "mission_id")
	o.ExpiresAt = optionalText(r, path, object, "expires_at")
	return o
}

func (r *reader) query(doc any) Query {
	var q Query
	object, ok := r.object("", doc)
	if !ok {
		return q
	}
	q.Repo, _ = field(r, "", object, "repo", required, r.text)
	q.Path, _ = field(r, "", object, "path", required, r.text)
	q.Action, _ = field(r, "", object, "action", required, choice(r, Read, Write, Exec, GitPush))
	q.Member, _ = field(r, "", object, "member", required, r.text)
	q.Now, _ = field(r, "", object, "now", required, r.timestamp)
	q.Command, _ = field(r, "", object, "command", object["action"] == string(Exec), r.text)
	q.AgentSessionID = optionalText(r, "", object, "agent_session_id")
	q.MissionID = optionalText(r, "", object, "mission_id")
	if parent, present := field(r, "", object, "parent", optional, r.parent); present {
		q.Parent = &parent
	}
	q.Overrides, _ = field(r, "", object, "overrides", optional, list(r, r.override))
	r.rejectUnknownFields("", object, "repo", "path", "action", "member", "now", "command", "agent_session_id", "mission_id", "parent", "overrides")
	return q
}

func (r *reader) parent(path string, value any) Parent {
	var p Parent
	object, ok := r.object(path, value)
	if !ok {
		return p
	}
	p.AgentSessionID, _ = field(r, path, object, "agent_session_id", required, r.text)
	p.MissionID = optionalText(r, path, object, "mission_id")
	r.rejectUnknownFields(path, object, "agent_session_id", "mission_id")
	return p
}
