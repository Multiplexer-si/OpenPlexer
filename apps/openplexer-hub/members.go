package main

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/multiplexer-si/openplexer/internal/hub"
)

func (h remoteAdmin) listMembers(project, after string) (hub.MembersPage, error) {
	query := url.Values{}
	if project != "" {
		query.Set("project_id", project)
	}
	if after != "" {
		query.Set("after", after)
	}
	path := adminPath + "/members"
	if len(query) != 0 {
		path += "?" + query.Encode()
	}
	res, err := h.request(http.MethodGet, path, nil)
	if err != nil {
		return hub.MembersPage{}, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, remoteAdminLimit+1))
	var page hub.MembersPage
	if err != nil || len(data) > remoteAdminLimit || jsonv2.Unmarshal(data, &page) != nil || page.Members == nil {
		return hub.MembersPage{}, errors.New("invalid members response from hub")
	}
	return page, nil
}

func (h remoteAdmin) policyVersion(project string) (int64, error) {
	res, err := h.request(http.MethodHead, "/api/v1/projects/"+project+"/policy.bundle", nil)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()
	values := res.Header.Values(hub.PolicyVersionHeader)
	if len(values) != 1 {
		return 0, errors.New("invalid policy version header from hub")
	}
	version, err := strconv.ParseInt(values[0], 10, 64)
	if err != nil {
		return 0, errors.New("invalid policy version header from hub")
	}
	return version, nil
}

func printMembers(out io.Writer, project string, now time.Time,
	list func(string, string) (hub.MembersPage, error), currentVersion func(string) (int64, error),
) error {
	if project != "" && !remoteID.MatchString(project) {
		return errors.New("--project must be a valid project ID")
	}
	invalidPage := errors.New("invalid members response from hub")
	versions := make(map[string]int64)
	var lastMember, lastProject, after string
	for {
		page, err := list(project, after)
		if err != nil {
			return err
		}
		for _, member := range page.Members {
			projectID := ""
			if member.ProjectID != nil {
				projectID = *member.ProjectID
				if !remoteID.MatchString(projectID) {
					return invalidPage
				}
			}
			if !remoteID.MatchString(member.MemberID) || member.HubRole != hub.Lead && member.HubRole != hub.Member ||
				project != "" && projectID != project ||
				member.MemberID < lastMember || member.MemberID == lastMember && projectID <= lastProject {
				return invalidPage
			}
			lastMember, lastProject = member.MemberID, projectID
			if (member.PolicyVersion == nil) != (member.LastPolicyFetchAt == nil) ||
				projectID == "" && (member.PolicyVersion != nil || member.LastRoomConnectedAt != nil) {
				return invalidPage
			}
			parts := []string{member.MemberID, "role " + string(member.HubRole)}
			if member.LastPolicyFetchAt != nil {
				at, err := time.Parse(time.RFC3339Nano, *member.LastPolicyFetchAt)
				if err != nil {
					return invalidPage
				}
				version, ok := versions[projectID]
				if !ok {
					version, err = currentVersion(projectID)
					if err != nil {
						return err
					}
					versions[projectID] = version
				}
				policy := fmt.Sprintf("policy v%d", *member.PolicyVersion)
				if *member.PolicyVersion != version {
					policy += fmt.Sprintf(" (current v%d)", version)
				}
				parts = append(parts, policy, "last check-in "+observationAge(now.Sub(at)))
			}
			if member.LastRoomConnectedAt != nil {
				at, err := time.Parse(time.RFC3339Nano, *member.LastRoomConnectedAt)
				if err != nil {
					return invalidPage
				}
				parts = append(parts, "last Room connection "+observationAge(now.Sub(at)))
			}
			if len(parts) == 2 {
				parts = append(parts, "no recent observation")
			}
			if project == "" && projectID != "" {
				parts = append(parts, "project "+projectID)
			}
			if _, err := fmt.Fprintln(out, strings.Join(parts, "  ")); err != nil {
				return err
			}
		}
		if page.NextCursor == nil {
			return nil
		}
		if *page.NextCursor == "" || *page.NextCursor == after || len(page.Members) == 0 {
			return invalidPage
		}
		after = *page.NextCursor
	}
}

func observationAge(age time.Duration) string {
	for _, unit := range []struct {
		duration time.Duration
		name     string
	}{{24 * time.Hour, "day"}, {time.Hour, "hour"}, {time.Minute, "minute"}} {
		if count := age / unit.duration; count > 0 {
			name := unit.name
			if count > 1 {
				name += "s"
			}
			return fmt.Sprintf("%d %s ago", count, name)
		}
	}
	return "just now"
}
