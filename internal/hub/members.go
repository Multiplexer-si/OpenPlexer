package hub

import (
	"encoding/base32"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

const observationRetention = 30 * 24 * time.Hour

type observation struct {
	LastPolicyFetchAt   *string `json:"last_policy_fetch_at"`
	PolicyVersion       *int64  `json:"policy_version"`
	LastRoomConnectedAt *string `json:"last_room_connected_at"`
}

func (o observation) retained(cutoff string) observation {
	if o.LastPolicyFetchAt != nil && *o.LastPolicyFetchAt <= cutoff {
		o.LastPolicyFetchAt, o.PolicyVersion = nil, nil
	}
	if o.LastRoomConnectedAt != nil && *o.LastRoomConnectedAt <= cutoff {
		o.LastRoomConnectedAt = nil
	}
	return o
}

func (o observation) empty() bool {
	return o.LastPolicyFetchAt == nil && o.LastRoomConnectedAt == nil
}

type memberObservations map[string]observation

func (s *Store) observationsDir() string { return filepath.Join(s.dir, "member-sync") }

var observationFileEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

func (s *Store) observationsPath(memberID string) string {
	// Member IDs are case-sensitive, including on case-insensitive volumes.
	return filepath.Join(s.observationsDir(), observationFileEncoding.EncodeToString([]byte(memberID))+".json")
}

func readObservations(path string) (memberObservations, error) {
	observations := make(memberObservations)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return observations, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &observations); err != nil {
		return nil, err
	}
	if observations == nil {
		return nil, errors.New("invalid member observations")
	}
	return observations, nil
}

func removeObservations(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// observe runs under the store lock, after the caller authenticates the active credential.
func (s *Store) observe(memberID, projectID string, version *int64) error {
	dir := s.observationsDir()
	path := s.observationsPath(memberID)
	all, err := readObservations(path)
	if err != nil {
		return err
	}
	o := all[projectID]
	at := s.now().UTC().Format(timestampFmt)
	if version == nil {
		o.LastRoomConnectedAt = &at
	} else {
		o.LastPolicyFetchAt, o.PolicyVersion = &at, version
	}
	all[projectID] = o
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return saveJSON(dir, path, all)
}

func (s *Store) observationCutoff() string {
	return s.now().UTC().Add(-observationRetention).Format(timestampFmt)
}

// PruneMemberObservations removes expired values and abandoned temporary files.
func (s *Store) PruneMemberObservations() error {
	return s.locked(func() error {
		dir := s.observationsDir()
		files, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		cutoff := s.observationCutoff()
		for _, file := range files {
			name := file.Name()
			path := filepath.Join(dir, name)
			if strings.HasPrefix(name, ".snapshot-") && file.Type().IsRegular() {
				if err := removeObservations(path); err != nil {
					return err
				}
				continue
			}
			if !file.Type().IsRegular() || !strings.HasSuffix(name, ".json") {
				continue
			}
			all, err := readObservations(path)
			if err != nil {
				return err
			}
			changed := false
			for projectID, old := range all {
				o := old.retained(cutoff)
				if o.empty() {
					delete(all, projectID)
					changed = true
				} else if o != old {
					all[projectID] = o
					changed = true
				}
			}
			switch {
			case len(all) == 0:
				err = removeObservations(path)
			case changed:
				err = saveJSON(dir, path, all)
			}
			if err != nil {
				return err
			}
		}
		return nil
	})
}

const memberPageSize = 100

type MemberSync struct {
	MemberID  string  `json:"member_id"`
	HubRole   Role    `json:"hub_role"`
	ProjectID *string `json:"project_id"`
	observation
}

type MembersPage struct {
	Members    []MemberSync `json:"members"`
	NextCursor *string      `json:"next_cursor"`
}

type memberCursor [2]*string

func (c memberCursor) encode() string {
	data, _ := json.Marshal(c)
	return base64URL(data)
}

func parseMemberCursor(encoded string) (memberCursor, error) {
	var cursor memberCursor
	data, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || json.Unmarshal(data, &cursor) != nil || cursor[0] == nil ||
		!idPattern.MatchString(*cursor[0]) || cursor[1] != nil && !idPattern.MatchString(*cursor[1]) ||
		cursor.encode() != encoded {
		return memberCursor{}, fmt.Errorf("%w: malformed member cursor", errInvalid)
	}
	return cursor, nil
}

type membersQuery struct {
	project string
	after   memberCursor
	limit   int
}

func parseMembersQuery(raw string) (membersQuery, error) {
	query, err := url.ParseQuery(raw)
	if err != nil {
		return membersQuery{}, fmt.Errorf("%w: invalid query string", errInvalid)
	}
	q := membersQuery{limit: memberPageSize}
	for key, values := range query {
		if len(values) != 1 {
			return membersQuery{}, fmt.Errorf("%w: repeated member query parameter", errInvalid)
		}
		value := values[0]
		switch key {
		case "project_id":
			if !idPattern.MatchString(value) {
				return membersQuery{}, fmt.Errorf("%w: invalid project_id", errInvalid)
			}
			q.project = value
		case "after":
			q.after, err = parseMemberCursor(value)
			if err != nil {
				return membersQuery{}, err
			}
		case "limit":
			limit, err := strconv.ParseUint(value, 10, 64)
			if err != nil || limit < 1 || limit > memberPageSize {
				return membersQuery{}, fmt.Errorf("%w: limit must be from 1 through 100", errInvalid)
			}
			q.limit = int(limit)
		default:
			return membersQuery{}, fmt.Errorf("%w: unknown member query parameter", errInvalid)
		}
	}
	return q, nil
}

// ListMembers reads a page for a local data directory operator.
func (s *Store) ListMembers(project, after string) (MembersPage, error) {
	params := url.Values{}
	if project != "" {
		params.Set("project_id", project)
	}
	if after != "" {
		params.Set("after", after)
	}
	q, err := parseMembersQuery(params.Encode())
	if err != nil {
		return MembersPage{}, err
	}
	var page MembersPage
	err = s.view(func(st *state) error {
		page, err = s.membersPage(st, q)
		return err
	})
	return page, err
}

func (s *Store) membersPage(st *state, q membersQuery) (MembersPage, error) {
	if _, ok := st.Projects[q.project]; q.project != "" && !ok {
		return MembersPage{}, errNotFound
	}
	cutoff := s.observationCutoff()
	ids := make([]string, 0, len(st.Members))
	for id := range st.Members {
		if q.after[0] == nil || id >= *q.after[0] {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)

	page := MembersPage{Members: make([]MemberSync, 0, q.limit)}
	for _, id := range ids {
		all, err := readObservations(s.observationsPath(id))
		if err != nil {
			return MembersPage{}, err
		}
		projects := []string{q.project}
		if q.project == "" {
			projects = projects[:0]
			for project, o := range all {
				if !o.retained(cutoff).empty() {
					projects = append(projects, project)
				}
			}
			if len(projects) == 0 {
				projects = append(projects, "")
			}
			slices.Sort(projects)
		}
		for _, project := range projects {
			if q.after[0] != nil && id == *q.after[0] {
				if project == "" || q.after[1] != nil && project <= *q.after[1] {
					continue
				}
			}
			if len(page.Members) == q.limit {
				last := page.Members[len(page.Members)-1]
				next := memberCursor{&last.MemberID, last.ProjectID}.encode()
				page.NextCursor = &next
				return page, nil
			}
			row := MemberSync{MemberID: id, HubRole: st.Members[id].Role, observation: all[project].retained(cutoff)}
			if project != "" {
				row.ProjectID = &project
			}
			page.Members = append(page.Members, row)
		}
	}
	return page, nil
}

func (s *Store) membersEndpoint() http.HandlerFunc {
	return s.inviteEndpoint("GET, HEAD, POST", true, func(st *state, req request) (int, any, error) {
		if req.Method == http.MethodPost {
			var input struct {
				ID   string `json:"id"`
				Role Role   `json:"role"`
			}
			if err := decodeInviteBody(req.body, &input); err != nil {
				return 0, nil, errInvalidMember
			}
			token, err := st.addMember(input.ID, input.Role, &req.member, s.now())
			if err == nil {
				err = s.save(st)
			}
			return http.StatusOK, map[string]string{"token": token}, err
		}
		if len(req.body) != 0 {
			return 0, nil, fmt.Errorf("%w: unexpected request body", errInvalid)
		}
		q, err := parseMembersQuery(req.URL.RawQuery)
		if err != nil {
			return 0, nil, err
		}
		page, err := s.membersPage(st, q)
		return http.StatusOK, page, err
	})
}
