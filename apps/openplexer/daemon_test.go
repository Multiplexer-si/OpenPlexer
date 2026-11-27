package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/multiplexer-si/openplexer/internal/room"
)

func TestQueuedDenialNotice(t *testing.T) {
	const at = "2026-01-15T12:00:00.000Z"
	for _, tc := range []struct {
		name, decidedAt string
		queued          bool
	}{
		{name: "older hook"},
		{name: "same time", decidedAt: at},
		{name: "one interval", decidedAt: "2026-01-15T11:59:59.000Z"},
		{name: "just over one interval", decidedAt: "2026-01-15T11:59:58.999Z", queued: true},
		{name: "one hour offline", decidedAt: "2026-01-15T11:00:00.000Z", queued: true},
		{name: "hook clock ahead", decidedAt: "2026-01-15T12:00:02.000Z", queued: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := `"action":"write","path":"src/main.go","rule":"locked"`
			if tc.decidedAt != "" {
				body += fmt.Sprintf(`,"decided_at":%q`, tc.decidedAt)
			}
			event, err := room.ParseEvent(fmt.Appendf(nil, `{"id":"denial","seq":1,"mission":"project","type":"policy.denied","agent":{"id":"session","vendor":"codex"},"on_behalf_of":"member","at":%q,"body":{%s}}`, at, body))
			if err != nil {
				t.Fatal(err)
			}
			var log bytes.Buffer
			reportQueuedDenial(&log, event)
			if !tc.queued {
				if log.Len() != 0 {
					t.Fatalf("unexpected notice %q", log.String())
				}
				return
			}
			if got := log.String(); !strings.Contains(got, "was queued") ||
				!strings.Contains(got, "decided at "+tc.decidedAt) || !strings.Contains(got, "received at "+at) {
				t.Fatalf("notice = %q, want queued denial with both times", got)
			}
		})
	}
}
