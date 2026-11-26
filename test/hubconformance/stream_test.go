package hubconformance

import (
	"bufio"
	"encoding/json/jsontext"
	"net/http"
	"testing"
)

const ndjson = "application/x-ndjson"

func streamPath(id string) string { return "/api/v1/rooms/" + id + "/stream" }

func (f *fixture) liveStream(t *testing.T) {
	id := *secondProject
	path := streamPath(id)
	t.Run("auth-and-cursor", func(t *testing.T) {
		for _, token := range []string{"", "short", f.unknown} {
			f.call(t, get, path, token, "", http.StatusUnauthorized)
		}
		if f.revoked != "" {
			f.call(t, get, path, f.revoked, "", http.StatusUnauthorized)
		}
		f.call(t, get, streamPath(f.missing), f.member, "", http.StatusNotFound)
		for _, query := range []string{"?last_seq=", "?last_seq=-1", "?last_seq=1.5", "?last_seq=0&last_seq=1",
			"?last_seq=0&%6cast_seq=0", "?bad=%ZZ", "?last_seq=9007199254740992"} {
			f.call(t, get, path+query, f.member, "", http.StatusBadRequest)
		}
	})
	t.Run("legacy-post", func(t *testing.T) {
		res, err := f.send(t.Context(), post, path, f.member, "", "")
		if err != nil {
			t.Fatalf("POST stream: %s", f.redact(err.Error()))
		}
		if f.target == localWrangler || f.target == externalHub && res.status == http.StatusMethodNotAllowed {
			res.expectJSON(t, post, path)
			if res.status != http.StatusMethodNotAllowed || res.header.Get("Allow") != "GET, HEAD" {
				t.Fatalf("POST stream returned %d with Allow %q, want 405 with GET and HEAD", res.status, res.header.Get("Allow"))
			}
			return
		}
		checkStreamHeaders(t, res.status, res.header)
		if string(res.body) != "\n" {
			t.Fatal("legacy stream should send a heartbeat before closing on request EOF")
		}
	})
	t.Run("backlog-live-and-reconnect", func(t *testing.T) {
		publish := func() []byte {
			t.Helper()
			return f.call(t, post, eventsPath(id), f.member, statusPost, http.StatusOK).body
		}
		start := eventFrom(t, publish(), id).Seq()
		backlog := make([][]byte, 101)
		for i := range backlog {
			backlog[i] = publish()
		}
		scan, closeStream := f.openStream(t, path+cursor(start))
		defer closeStream()
		expectStreamEvent(t, streamLine(t, scan), id, backlog[0], start+1)
		duringReplay := publish()
		for i, raw := range backlog[1:] {
			expectStreamEvent(t, streamLine(t, scan), id, raw, start+int64(i)+2)
		}
		seq := start + int64(len(backlog)) + 1
		expectStreamEvent(t, nextEventLine(t, scan), id, duringReplay, seq)
		for range 2 {
			if frame := streamLine(t, scan); len(frame) != 0 {
				t.Fatal("expected a blank heartbeat after replay, got an extra frame")
			}
		}
		live := publish()
		expectStreamEvent(t, nextEventLine(t, scan), id, live, seq+1)
		closeStream()
		missed := publish()
		resumed, closeResumed := f.openStream(t, path+cursor(seq+1))
		defer closeResumed()
		expectStreamEvent(t, streamLine(t, resumed), id, missed, seq+2)
		if frame := streamLine(t, resumed); len(frame) != 0 {
			t.Fatal("expected a heartbeat after the missed event")
		}
	})
	t.Run("ahead-cursor", func(t *testing.T) {
		scan, closeStream := f.openStream(t, path+cursor(1<<53-1))
		defer closeStream()
		if frame := streamLine(t, scan); len(frame) != 0 {
			t.Fatal("stream with an ahead cursor should start with a heartbeat")
		}
	})
}

func (f *fixture) openStream(t *testing.T, path string) (*bufio.Scanner, func()) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), get, f.origin+path, nil)
	if err != nil {
		t.Fatal("could not build the stream request")
	}
	req.Header.Set("Authorization", "Bearer "+f.member)
	res, err := f.client.Do(req)
	if err != nil {
		t.Fatalf("open Room stream: %s", f.redact(err.Error()))
	}
	closeStream := func() { _ = res.Body.Close() }
	t.Cleanup(closeStream)
	checkStreamHeaders(t, res.StatusCode, res.Header)
	scan := bufio.NewScanner(res.Body)
	scan.Buffer(make([]byte, 4096), 1<<20)
	return scan, closeStream
}

func checkStreamHeaders(t *testing.T, status int, headers http.Header) {
	t.Helper()
	if status != http.StatusOK || headers.Get("Content-Type") != ndjson || headers.Get("Cache-Control") != "no-store" {
		t.Fatalf("stream returned %d with Content-Type %q and Cache-Control %q", status,
			headers.Get("Content-Type"), headers.Get("Cache-Control"))
	}
}

func streamLine(t *testing.T, scan *bufio.Scanner) []byte {
	t.Helper()
	if !scan.Scan() {
		t.Fatalf("stream ended while waiting for a line (%T)", scan.Err())
	}
	return scan.Bytes()
}

func nextEventLine(t *testing.T, scan *bufio.Scanner) []byte {
	t.Helper()
	for {
		if frame := streamLine(t, scan); len(frame) != 0 {
			return frame
		}
	}
}

func expectStreamEvent(t *testing.T, frame []byte, id string, raw []byte, seq int64) {
	t.Helper()
	if !jsontext.Value(frame).IsValid() || !jsontext.Value(raw).IsValid() ||
		eventFrom(t, frame, id).Seq() != seq || !sameJSON(t, frame, raw) {
		t.Fatalf("stream event %d differs from the committed envelope", seq)
	}
}
