package hub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/multiplexer-si/openplexer/internal/audit"
)

func BenchmarkMemberObservationDisk(b *testing.B) {
	const members, projects, eventsPerRoom = 50, 5, 1000
	type observation struct {
		FetchAt time.Time `json:"last_policy_fetch_at"`
		Version int64     `json:"policy_version"`
		RoomAt  time.Time `json:"last_room_connected_at"`
	}
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	s := Open(b.TempDir())
	if err := s.Init(); err != nil {
		b.Fatal(err)
	}
	st, err := s.load()
	if err != nil {
		b.Fatal(err)
	}

	team := make(map[string]map[string]observation)
	for i := range members {
		id := fmt.Sprintf("mem-%02d", i)
		st.Members[id] = member{Role: Member}
		if err := st.appendAudit(audit.Record{Action: "member.added", Target: "member:" + id}, now); err != nil {
			b.Fatal(err)
		}
		team[id] = make(map[string]observation)
		for j := range projects {
			team[id][fmt.Sprintf("project-%d", j)] = observation{now, 42, now}
		}
	}
	for i := range projects {
		id := fmt.Sprintf("project-%d", i)
		st.Projects[id] = Project{ID: id, Repo: "team/" + id}
		for seq := range eventsPerRoom {
			member := fmt.Sprintf("mem-%02d", seq%members)
			sender := agentInput{ID: member + "/agent", Vendor: "codex"}
			kind, body := "status", map[string]any{
				"summary": "Reviewing route validation and updating the project tests for the next release.",
			}
			if seq%20 == 0 {
				kind, body = "policy.denied", map[string]any{
					"action": "write", "path": "src/release/settings.go", "rule": "denied by policy",
				}
			}
			at := now.Add(time.Duration(seq) * time.Minute)
			event, err := st.appendEvent(id, member, sender, kind, body, at, "")
			if err != nil {
				b.Fatal(err)
			}
			if kind == "policy.denied" {
				if err := st.appendAudit(audit.Record{
					Action: "event.policy-denied", Target: "event:" + event.ID(),
					ActorMemberID: &member, AgentSessionID: &sender.ID,
					Meta: map[string]any{"room_id": id, "seq": event.Seq(), "event_id": event.ID()},
				}, at); err != nil {
					b.Fatal(err)
				}
			}
		}
	}

	for _, tc := range []struct {
		name string
		data any
	}{
		{"room_and_audit_snapshot", struct {
			*state
			Observations any `json:"observations"`
		}{st, team}},
		{"team_sidecar", team},
		{"member_sidecar", team["mem-00"]},
	} {
		b.Run(tc.name, func(b *testing.B) {
			dir := b.TempDir()
			path := filepath.Join(dir, "snapshot.json")
			for i := range b.N {
				team["mem-00"]["project-0"] = observation{now.Add(time.Duration(i) * time.Millisecond), 42, now}
				if err := saveJSON(dir, path, tc.data); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			data, err := os.ReadFile(path)
			if err != nil {
				b.Fatal(err)
			}
			var expected bytes.Buffer
			if err := json.NewEncoder(&expected).Encode(tc.data); err != nil || !bytes.Equal(data, expected.Bytes()) {
				b.Fatal("disk did not contain the updated snapshot")
			}
			b.ReportMetric(float64(len(data)), "bytes/write")
		})
	}
}
