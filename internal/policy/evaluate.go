package policy

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Evaluate decides one action against a bundle, under the rules of
// spec/policy/README.md. It reads no file, no network, and no clock. Every
// result has a rule, which gives the reason. With no bundle, the result is
// deny.
func Evaluate(bundle *Bundle, q Query) Result {
	return NewBudget().Evaluate(bundle, q)
}

// maxWork bounds the work of all file queries and repositories of a call.
// A unit covers one scan step, emitted instruction, matcher loop iteration,
// or predicate check, including a comparison with one span of a class.
// The end-to-end measurements are in spec/policy/README.md.
const maxWork = 60_000_000

var errOverBudget = fmt.Errorf("the evaluation is over its budget: the globs that apply to the path, matched against it, are more than %d steps of work", maxWork)

// A Budget is the work that the evaluations of one call of an agent may
// still do. An evaluation that would go over the budget denies.
type Budget struct{ left int }

// NewBudget returns the budget of one call.
func NewBudget() *Budget { return &Budget{left: maxWork} }

func (b *Budget) spend(work int) error {
	if work > b.left {
		b.left = 0
		return errOverBudget
	}
	b.left -= work
	return nil
}

// Evaluate is [Evaluate] within the budget b.
func (b *Budget) Evaluate(bundle *Bundle, q Query) Result {
	if bundle == nil {
		return deny("there is no policy bundle")
	}
	roleName, role := bundle.roleOf(q.Member)
	switch q.Action {
	case Exec:
		return evaluateExec(q, roleName, role)
	case GitPush:
		return evaluatePush(q, roleName, role)
	case Read, Write:
		return evaluateFile(bundle, q, roleName, role, b)
	}
	return deny(fmt.Sprintf("%q is not an action", q.Action))
}

func allow(rule string) Result { return Result{Decision: Allow, Rule: rule} }
func deny(rule string) Result  { return Result{Decision: Deny, Rule: rule} }

func (r Result) in(area *Area) Result {
	name := area.Name
	r.Area = &name
	return r
}

func (b *Bundle) roleOf(member string) (string, *Role) {
	name, ok := b.Members[member]
	if !ok {
		return "", nil
	}
	role, ok := b.Roles[name]
	if !ok {
		return name, nil
	}
	return name, &role
}

func noRole(q Query, roleName string) string {
	if roleName == "" {
		return fmt.Sprintf("member %s has no role", q.Member)
	}
	return fmt.Sprintf("member %s has the role %s, which the bundle does not define", q.Member, roleName)
}

func evaluateExec(q Query, roleName string, role *Role) Result {
	if strings.ContainsAny(q.Command, ";&|<>`\n\r") || strings.Contains(q.Command, "$(") {
		return deny("command contains shell chaining or redirection and cannot be matched to an exec grant")
	}
	if role == nil {
		return deny(noRole(q, roleName))
	}
	command := strings.TrimFunc(q.Command, isWhiteSpace)
	granted := slices.ContainsFunc(role.Exec, func(grant string) bool {
		rest, ok := strings.CutPrefix(command, grant)
		if !ok {
			return false
		}
		next, _ := utf8.DecodeRuneInString(rest)
		return rest == "" || isWhiteSpace(next)
	})
	if granted {
		return allow(fmt.Sprintf("command is granted to role %s", roleName))
	}
	return deny(fmt.Sprintf("command is not in the exec grants of role %s", roleName))
}

// isWhiteSpace reports the 25 code points that spec/README.md lists: the
// white space and the line terminators of ECMA-262.
func isWhiteSpace(r rune) bool {
	switch {
	case 0x0009 <= r && r <= 0x000D, 0x2000 <= r && r <= 0x200A:
		return true
	case r == 0x0020, r == 0x00A0, r == 0x1680, r == 0x2028, r == 0x2029, r == 0x202F, r == 0x205F, r == 0x3000, r == 0xFEFF:
		return true
	}
	return false
}

func evaluatePush(q Query, roleName string, role *Role) Result {
	if role == nil {
		return deny(fmt.Sprintf("git push to %s is not granted (%s)", q.Repo, noRole(q, roleName)))
	}
	if slices.Contains(role.Push, "*") || slices.Contains(role.Push, q.Repo) {
		return allow(fmt.Sprintf("git push to %s is granted to role %s", q.Repo, roleName))
	}
	return deny(fmt.Sprintf("git push to %s is not granted to role %s", q.Repo, roleName))
}

func evaluateFile(bundle *Bundle, q Query, roleName string, role *Role, budget *Budget) Result {
	if err := budget.spend(len(q.Path) + len(q.Repo) + 1); err != nil {
		return deny(err.Error())
	}
	path, ok := canonicalPath(q.Path)
	if !ok {
		return deny("path is absolute or escapes the project root")
	}
	if utf16Length(path) > MaxGlobPath {
		return deny(errPathTooLong.Error())
	}
	matched, err := bundle.matchAreas(q.Repo, path, budget)
	if err != nil {
		return deny(err.Error())
	}

	if q.Action == Write {
		for _, m := range matched {
			if m.area.System {
				return deny(fmt.Sprintf("area %q is a system area and is never writable", m.area.Name)).in(m.area)
			}
		}
	}

	var winner *verdict
	for _, m := range matched {
		v := verdict{match: m}
		if err := budget.spend(grantWork(m.area, q, role, len(bundle.TemporaryGrants))); err != nil {
			return deny(err.Error())
		}
		v.source, v.allowed = bundle.grantFor(m.area, q, role)
		if !v.allowed && m.area.Private {
			winner = &v
			break
		}
		if winner == nil || v.replaces(*winner) {
			winner = &v
		}
	}

	switch {
	case winner == nil && q.Action == Read:
		return allow("path is in no area, and a read is allowed by default")
	case winner == nil:
		return deny("path is in no area, and a write is denied by default")
	case winner.allowed:
		return allow(winner.allowRule(q, roleName)).in(winner.area)
	}
	return deny(bundle.denyRule(winner.area, q, roleName, role)).in(winner.area)
}

func canonicalPath(raw string) (string, bool) {
	path := strings.ReplaceAll(raw, `\`, "/")
	if strings.HasPrefix(path, "/") || (len(path) >= 2 && isASCIILetter(path[0]) && path[1] == ':') {
		return "", false
	}
	var segments []string
	for segment := range strings.SplitSeq(path, "/") {
		switch segment {
		case "", ".":
		case "..":
			if len(segments) == 0 {
				return "", false
			}
			segments = segments[:len(segments)-1]
		default:
			segments = append(segments, segment)
		}
	}
	return strings.Join(segments, "/"), true
}

func isASCIILetter(b byte) bool {
	return 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z'
}

type specificity struct {
	literalPrefix int
	segments      int
}

func specificityOf(glob string) specificity {
	prefix := glob
	if i := strings.IndexAny(glob, "*?["); i != -1 {
		prefix = glob[:i]
	}
	return specificity{literalPrefix: utf16Length(prefix), segments: strings.Count(glob, "/") + 1}
}

func (s specificity) moreThan(other specificity) bool {
	return s.literalPrefix > other.literalPrefix || (s.literalPrefix == other.literalPrefix && s.segments > other.segments)
}

type match struct {
	area *Area
	specificity
}

func (b *Bundle) matchAreas(repo, path string, budget *Budget) ([]match, error) {
	repoLower, pathLower := lowercase(repo), lowercase(path)
	units := utf16.Encode([]rune(path))
	var scratch globScratch
	var matched []match
	for i := range b.Areas {
		area := &b.Areas[i]
		if err := budget.spend(1); err != nil {
			return nil, err
		}
		var best *specificity
		for _, pattern := range area.Patterns {
			if err := budget.spend(len(pattern) + 1); err != nil {
				return nil, err
			}
			patternRepo, glob, found := strings.Cut(pattern, ":")
			if !found {
				patternRepo, glob = "*", pattern
			}
			if patternRepo != "*" && lowercase(patternRepo) != repoLower {
				continue
			}
			glob = NormalizeGlob(glob)
			directory, isTree := strings.CutSuffix(glob, "/**")
			if !isTree || lowercase(directory) != pathLower {
				matches, err := scratch.matches(glob, units, true, budget, nil)
				if errors.Is(err, errOverBudget) {
					return nil, err
				}
				if err != nil {
					return nil, fmt.Errorf("pattern %q of area %q is not a glob: %w", pattern, area.Name, err)
				}
				if !matches {
					continue
				}
			}
			if s := specificityOf(glob); best == nil || s.moreThan(*best) {
				best = &s
			}
		}
		if best != nil {
			matched = append(matched, match{area: area, specificity: *best})
		}
	}
	return matched, nil
}

type verdict struct {
	match
	allowed bool
	source  string
}

func (v verdict) replaces(winner verdict) bool {
	deniesOnTie := v.specificity == winner.specificity && winner.allowed && !v.allowed
	return v.specificity.moreThan(winner.specificity) || deniesOnTie
}

const (
	byDefault        = "default"
	byRole           = "role"
	byTemporaryGrant = "temporary grant"
	byOverride       = "override"
)

func grantWork(area *Area, q Query, role *Role, temporary int) int {
	if role == nil || q.Action == Read && !area.Private {
		return 1
	}
	grants := role.Read
	if q.Action == Write {
		grants = role.Write
	}
	work := int64(len(grants)) * (1 + int64(len(area.Name)))
	if !area.System {
		work += int64(temporary+len(q.Overrides)) * (1 + int64(len(area.Name)) + int64(len(q.Member)))
	}
	return int(min(work+1, maxWork+1))
}

func (b *Bundle) grantFor(area *Area, q Query, role *Role) (source string, allowed bool) {
	switch {
	case q.Action == Write && role != nil && slices.Contains(role.Write, area.Name):
		return byRole, true
	case q.Action == Read && !area.Private:
		return byDefault, true
	case q.Action == Read && role != nil && slices.Contains(role.Read, area.Name):
		return byRole, true
	}
	canBeOpened := role != nil && !area.System
	if !canBeOpened {
		return "", false
	}
	holder := holderOf(q)
	switch {
	case slices.ContainsFunc(b.TemporaryGrants, func(g TemporaryGrant) bool { return g.applies(area.Name, q, holder) }):
		return byTemporaryGrant, true
	case slices.ContainsFunc(q.Overrides, func(o Override) bool { return o.applies(area.Name, q, holder) }):
		return byOverride, true
	}
	return "", false
}

type holder struct {
	session *string
	mission *string
}

func holderOf(q Query) holder {
	if q.Parent != nil {
		return holder{session: &q.Parent.AgentSessionID, mission: q.Parent.MissionID}
	}
	return holder{session: q.AgentSessionID, mission: q.MissionID}
}

func is(have *string, want string) bool {
	return have != nil && *have == want
}

func (g TemporaryGrant) applies(area string, q Query, h holder) bool {
	if g.MemberID != q.Member || g.Area != area || !slices.Contains(g.Actions, q.Action) {
		return false
	}
	switch g.Scope {
	case GrantForMission:
		return is(h.mission, g.MissionID)
	case GrantUntil:
		return q.Now.Before(g.ExpiresAt)
	}
	return false
}

func (o Override) applies(area string, q Query, h holder) bool {
	if o.Area != area || !slices.Contains(o.Actions, q.Action) {
		return false
	}
	if o.AgentSessionID != nil && !is(h.session, *o.AgentSessionID) {
		return false
	}
	if o.MissionID != nil && !is(h.mission, *o.MissionID) {
		return false
	}
	if o.ExpiresAt != nil {
		expires, ok := ParseTimestamp(*o.ExpiresAt)
		if !ok || !q.Now.Before(expires) {
			return false
		}
	}
	switch o.Scope {
	case OverrideOnce:
		return o.AgentSessionID != nil
	case OverrideForMission:
		return o.MissionID != nil
	case OverrideUntil:
		return o.ExpiresAt != nil
	}
	return false
}

func (v verdict) allowRule(q Query, roleName string) string {
	switch v.source {
	case byRole:
		return fmt.Sprintf("area %q %s is granted to role %s", v.area.Name, q.Action, roleName)
	case byDefault:
		return fmt.Sprintf("area %q is readable by default", v.area.Name)
	}
	return fmt.Sprintf("area %q %s is allowed by an active %s", v.area.Name, q.Action, v.source)
}

func (b *Bundle) denyRule(area *Area, q Query, roleName string, role *Role) string {
	var granted []string
	for _, name := range slices.Sorted(maps.Keys(b.Roles)) {
		grants := b.Roles[name].Read
		if q.Action == Write {
			grants = b.Roles[name].Write
		}
		if slices.Contains(grants, area.Name) {
			granted = append(granted, name)
		}
	}
	rule := fmt.Sprintf("area %q is private and not readable by any role", area.Name)
	switch {
	case q.Action == Write && len(granted) == 0:
		rule = fmt.Sprintf("area %q is not writable by any role", area.Name)
	case q.Action == Write:
		rule = fmt.Sprintf("area %q is writable by role %s only", area.Name, strings.Join(granted, ", "))
	case len(granted) > 0:
		rule = fmt.Sprintf("area %q is private; readable by role %s only", area.Name, strings.Join(granted, ", "))
	}
	if role == nil {
		rule += " (" + noRole(q, roleName) + ")"
	}
	return rule
}
