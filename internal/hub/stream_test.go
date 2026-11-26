package hub

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/multiplexer-si/openplexer/internal/room"
)

func TestRoomStreamAfterLegacyPost(t *testing.T) {
	const id = "demo"
	store, token := roomStore(t, id, "member")
	handler := store.Handler()
	body := []byte(`{"type":"human","body":{"text":"hello"}}`)
	wantStatus(t, call(handler, http.MethodPost, eventsURL(id), token, body, ""), http.StatusOK)

	synctest.Test(t, func(t *testing.T) {
		ctx, closeListener := context.WithCancel(t.Context())
		listener := &streamPipeListener{connections: make(chan net.Conn), ctx: ctx, cancel: closeListener}
		server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			handler.ServeHTTP(w, r)
			if r.Method == http.MethodPost {
				// Let the background read observe any expired deadline before net/http cleans up.
				synctest.Wait()
			}
		})}
		go func() { _ = server.Serve(listener) }()
		defer server.Close()
		transport := &http.Transport{DialContext: listener.dial}
		defer transport.CloseIdleConnections()
		client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
		url := "http://hub.test/api/v1/rooms/" + id + "/stream"
		request := func(method string) *http.Response {
			t.Helper()
			req, err := http.NewRequestWithContext(t.Context(), method, url, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer "+token)
			res, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { res.Body.Close() })
			if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != roomStreamType ||
				res.Header.Get("Cache-Control") != "no-store" || res.Header.Get("X-Accel-Buffering") != "no" {
				t.Fatalf("%s stream returned %d with headers %v", method, res.StatusCode, res.Header)
			}
			return res
		}

		post := request(http.MethodPost)
		if frames, err := io.ReadAll(post.Body); err != nil || string(frames) != "\n" {
			t.Fatalf("POST stream returned %q, %v, want one heartbeat and clean EOF", frames, err)
		}
		get := request(http.MethodGet)
		scanner := bufio.NewScanner(get.Body)
		if !scanner.Scan() {
			t.Fatalf("GET stream ended before replay: %v", scanner.Err())
		}
		message, err := room.ParseMessage(scanner.Bytes())
		if seq, ok := message.Seq(); err != nil || !ok || seq != 1 || message.Room() != id {
			t.Fatalf("unexpected replay frame %q: %v", scanner.Bytes(), err)
		}
		if !scanner.Scan() || len(scanner.Bytes()) != 0 {
			t.Fatalf("GET stream did not send a heartbeat after replay: %q, %v", scanner.Bytes(), scanner.Err())
		}
	})
}

type streamPipeListener struct {
	connections chan net.Conn
	ctx         context.Context
	cancel      context.CancelFunc
}

func (l *streamPipeListener) Accept() (net.Conn, error) {
	select {
	case conn := <-l.connections:
		return conn, nil
	case <-l.ctx.Done():
		return nil, net.ErrClosed
	}
}

func (l *streamPipeListener) Close() error {
	l.cancel()
	return nil
}

func (*streamPipeListener) Addr() net.Addr { return &net.TCPAddr{} }

func (l *streamPipeListener) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	client, server := net.Pipe()
	select {
	case l.connections <- server:
		return client, nil
	case <-l.ctx.Done():
	case <-ctx.Done():
	}
	client.Close()
	server.Close()
	return nil, net.ErrClosed
}
