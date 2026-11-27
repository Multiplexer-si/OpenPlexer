package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/multiplexer-si/openplexer/internal/hub"
)

func TestMembersCommandLocalAndRemote(t *testing.T) {
	dir := t.TempDir()
	s := hub.Open(dir)
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	lead, err := s.AddMember("mem-lee", hub.Lead)
	if err != nil {
		t.Fatal(err)
	}
	member, err := s.AddMember("mem-ana", hub.Member)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddProject("shop", "api"); err != nil {
		t.Fatal(err)
	}
	publish := func(version int) {
		t.Helper()
		bundle := fmt.Appendf(nil, `{"areas":[],"members":{},"project_id":"shop","repos":["api"],"roles":{},"version":%d}`, version)
		if _, err := s.PublishPolicy("shop", bundle); err != nil {
			t.Fatal(err)
		}
	}
	publish(1)
	h := s.Handler()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/projects/shop/policy.bundle", nil)
	request.Header.Set("Authorization", "Bearer "+member)
	res := httptest.NewRecorder()
	h.ServeHTTP(res, request)
	if res.Code != http.StatusOK {
		t.Fatalf("policy fetch returned %d", res.Code)
	}
	publish(2)

	for _, tc := range []struct {
		name string
		args []string
		rt   http.RoundTripper
	}{
		{"local", []string{"--data-dir", dir}, nil},
		{"remote", []string{"--hub", "https://hub.example.test"}, adminHandler(h)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, stderr bytes.Buffer
			args := append([]string{"members", "--project", "shop"}, tc.args...)
			code := runWithInput(args, strings.NewReader(lead), &out, &stderr, tc.rt)
			want := "mem-ana  role member  policy v1 (current v2)  last check-in just now\n" +
				"mem-lee  role lead  no recent observation\n"
			if code != 0 || out.String() != want || stderr.Len() != 0 {
				t.Fatalf("members returned %d or unexpected output", code)
			}
		})
	}
	page, err := s.ListMembers("shop", "")
	if err != nil || len(page.Members) != 2 || page.Members[1].LastPolicyFetchAt != nil {
		t.Fatal("listing members changed the lead's observation")
	}
}

func TestRemoteMembersFollowsPagesAndUsesPolicyHead(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	lead := testToken("member list")
	mux := http.NewServeMux()
	var calls []string
	addCall := func(r *http.Request) {
		t.Helper()
		if r.Header.Get("Authorization") != "Bearer "+lead {
			t.Fatal("missing lead authorization")
		}
		calls = append(calls, r.Method+" "+r.URL.Path)
	}
	mux.HandleFunc("GET /api/v1/admin/members", func(w http.ResponseWriter, r *http.Request) {
		addCall(r)
		if r.URL.Query().Get("project_id") != "shop" {
			t.Fatal("missing project filter")
		}
		cursor, id, roomAt := `"WyJtZW0tYW5hIiwic2hvcCJd"`, "mem-ana", "null"
		if r.URL.Query().Get("after") != "" {
			if r.URL.Query().Get("after") != "WyJtZW0tYW5hIiwic2hvcCJd" {
				t.Fatal("wrong page cursor")
			}
			cursor, id, roomAt = "null", "mem-bob", `"2026-10-04T11:00:00.000Z"`
		}
		fmt.Fprintf(w, `{"members":[{"member_id":%q,"hub_role":"member","project_id":"shop",
			"last_policy_fetch_at":"2026-10-01T12:00:00.000Z","policy_version":1,"last_room_connected_at":%s}],
			"next_cursor":%s}`, id, roomAt, cursor)
	})
	mux.HandleFunc("HEAD /api/v1/projects/shop/policy.bundle", func(w http.ResponseWriter, r *http.Request) {
		addCall(r)
		w.Header().Set(hub.PolicyVersionHeader, "2")
	})
	remote := remoteAdmin{origin: "https://hub.example.test", token: lead, client: &http.Client{Transport: adminHandler(mux)}}
	var out bytes.Buffer
	if err := printMembers(&out, "shop", now, remote.listMembers, remote.policyVersion); err != nil {
		t.Fatal(err)
	}
	want := "mem-ana  role member  policy v1 (current v2)  last check-in 3 days ago\n" +
		"mem-bob  role member  policy v1 (current v2)  last check-in 3 days ago  last Room connection 1 hour ago\n"
	if out.String() != want || !slices.Equal(calls, []string{
		"GET /api/v1/admin/members", "HEAD /api/v1/projects/shop/policy.bundle", "GET /api/v1/admin/members",
	}) {
		t.Fatal("unexpected rows or requests")
	}
}

func TestRemoteMembersRestrictedRead(t *testing.T) {
	dir := t.TempDir()
	s := hub.Open(dir)
	if err := s.Init(); err != nil {
		t.Fatal(err)
	}
	token, err := s.AddMember("ana", hub.Member)
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	code := runWithInput([]string{"members", "--hub", "https://hub.example.test"}, strings.NewReader(token),
		&out, &stderr, adminHandler(s.Handler()))
	if code != 1 || out.Len() != 0 || !strings.Contains(stderr.String(), "HTTP 403") || strings.Contains(stderr.String(), token) {
		t.Fatal("expected a credential-free 403")
	}
}
