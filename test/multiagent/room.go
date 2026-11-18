package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"time"
)

type fields map[string]any

type event struct {
	ID      string `json:"id"`
	Seq     int    `json:"seq"`
	Mission string `json:"mission"`
	Type    string `json:"type"`
	Member  string `json:"on_behalf_of"`
	At      string `json:"at"`
	Agent   struct{ ID, Vendor, Parent string }
	Body    fields
}

func (e event) equal(other event) bool {
	return reflect.DeepEqual(e, other)
}

func (e event) matches(kind string, p participant, session string, body fields) bool {
	if e.Type != kind || e.Member != p.member || e.Agent.ID != p.member+"/"+session || e.Agent.Vendor != p.vendor {
		return false
	}
	for key, value := range body {
		want, _ := json.Marshal(value)
		got, _ := json.Marshal(e.Body[key])
		if !bytes.Equal(got, want) {
			return false
		}
	}
	return true
}

func (r *run) origin() string { return "http://" + r.listen }

func (r *run) request(ctx context.Context, token, endpoint string, input []byte) ([]byte, error) {
	path := endpoint
	if token != "" {
		path = "/api/v1/rooms/" + projectID + endpoint
	}
	method := http.MethodGet
	if input != nil {
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, r.origin()+path, bytes.NewReader(input))
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s %s returned %s", method, path, res.Status)
	}
	return io.ReadAll(io.LimitReader(res.Body, 1<<20))
}

func (r *run) post(ctx context.Context, kind string, body fields) error {
	data, err := json.Marshal(fields{"type": kind, "agent": r.a.identity(r.session), "body": body})
	if err != nil {
		return err
	}
	_, err = r.cli(ctx, r.a, data, "room", "post")
	return err
}

func (r *run) hubCLI(ctx context.Context, command string, args ...string) ([]byte, error) {
	args = append([]string{command, "-data-dir", filepath.Join(r.dir, "hub-data")}, args...)
	return r.command(ctx, r.dir, serviceEnv(r.a), r.hubBin, nil, args...)
}

func (r *run) cli(ctx context.Context, p participant, input []byte, args ...string) ([]byte, error) {
	return r.command(ctx, p.project, serviceEnv(p), r.cliBin, input, args...)
}

func (r *run) startHub(ctx context.Context, logName string) error {
	var err error
	r.hub, err = r.start(ctx, r.dir, serviceEnv(r.a), r.hubBin, filepath.Join(r.dir, logName),
		"serve", "-data-dir", filepath.Join(r.dir, "hub-data"), "-listen", r.listen)
	if err != nil {
		return err
	}
	return waitFor(ctx, "hub listening at "+r.origin(), func(ctx context.Context) (bool, error) {
		if err := r.hub.running(); err != nil {
			return false, err
		}
		_, err := r.request(ctx, "", "/.well-known/jwks.json", nil)
		return err == nil, nil
	})
}

func (r *run) awaitEvent(ctx context.Context, p participant, seq int) (event, error) {
	var events []event
	process := r.daemonA
	if p.member == r.b.member {
		process = r.daemonB
	}
	err := waitFor(ctx, fmt.Sprintf("%s Room event %d", p.name, seq), func(context.Context) (bool, error) {
		if err := process.running(); err != nil {
			return false, err
		}
		var err error
		events, err = roomEvents(p)
		return len(events) >= seq, err
	})
	if err != nil {
		return event{}, err
	}
	return events[seq-1], nil
}

func (r *run) reconnect(ctx context.Context) error {
	before, err := r.awaitEvent(ctx, r.b, 3)
	if err != nil {
		return err
	}
	if err := awaitCursor(ctx, r.b, before.Seq); err != nil {
		return err
	}
	streamErrors := func() (int, error) {
		data, err := os.ReadFile(r.b.path("room.err"))
		return bytes.Count(data, []byte("Room stream:")), err
	}
	previous, err := streamErrors()
	if err != nil {
		return err
	}
	r.hub.stop()
	if err := waitFor(ctx, "B to observe the hub disconnect", func(context.Context) (bool, error) {
		if err := r.daemonB.running(); err != nil {
			return false, err
		}
		count, err := streamErrors()
		return count > previous, err
	}); err != nil {
		return err
	}
	// Hold B at its existing cursor until the hub has stored a new event.
	// B must replay that event on the same daemon process after it resumes.
	if err := r.daemonB.signal(syscall.SIGSTOP); err != nil {
		return err
	}
	queued := fields{"summary": "Queued while the hub was down"}
	if err := r.post(ctx, "status", queued); err != nil {
		return err
	}
	if err := r.startHub(ctx, "hub-restarted"); err != nil {
		return err
	}
	missed, err := r.awaitEvent(ctx, r.a, before.Seq+1)
	if err != nil {
		return err
	}
	if !missed.matches("status", r.a, r.session, queued) {
		return fmt.Errorf("A received the wrong queued event: %+v", missed)
	}
	events, err := roomEvents(r.b)
	if err != nil {
		return err
	}
	if len(events) != before.Seq {
		return fmt.Errorf("B received %d events while paused at %d", len(events), before.Seq)
	}
	if err := r.daemonB.signal(syscall.SIGCONT); err != nil {
		return err
	}
	replayed, err := r.awaitEvent(ctx, r.b, missed.Seq)
	if err != nil {
		return err
	}
	if !replayed.equal(missed) {
		return fmt.Errorf("B replayed the wrong queued event: %+v", replayed)
	}
	live := fields{"summary": "Posted after B reconnected"}
	if err := r.post(ctx, "status", live); err != nil {
		return err
	}
	seen, err := r.awaitEvent(ctx, r.b, before.Seq+2)
	if err != nil {
		return err
	}
	if !seen.matches("status", r.a, r.session, live) {
		return fmt.Errorf("B received the wrong live event: %+v", seen)
	}
	if err := awaitCursor(ctx, r.b, seen.Seq); err != nil {
		return err
	}
	return r.compareHistory(ctx, before.Seq+2)
}

func (r *run) compareHistory(ctx context.Context, count int) error {
	raw, err := r.request(ctx, r.b.token, "/events?last_seq=0", nil)
	if err != nil {
		return err
	}
	var replay []struct{ Payload event }
	if err := json.Unmarshal(raw, &replay); err != nil {
		return err
	}
	events, err := roomEvents(r.b)
	if err != nil {
		return err
	}
	if len(events) != count || len(replay) != count {
		return fmt.Errorf("expected %d Room events, B received %d and hub stored %d", count, len(events), len(replay))
	}
	for i, actual := range events {
		if !actual.equal(replay[i].Payload) {
			return fmt.Errorf("B Room event %d differs from hub history", i+1)
		}
	}
	return nil
}

func roomEvents(p participant) ([]event, error) {
	var events []event
	if err := readLines(p.path("room.jsonl"), &events); err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	for i, ev := range events {
		if ev.Seq != i+1 || ev.ID == "" || seen[ev.ID] || ev.Mission != projectID {
			return nil, fmt.Errorf("%s Room log has a missing, duplicate, or foreign event at line %d (seq %d)", p.name, i+1, ev.Seq)
		}
		seen[ev.ID] = true
	}
	return events, nil
}

func awaitCursor(ctx context.Context, p participant, seq int) error {
	return waitFor(ctx, fmt.Sprintf("%s Room cursor %d", p.name, seq), func(context.Context) (bool, error) {
		raw, err := os.ReadFile(filepath.Join(p.home, "room-cursor.json"))
		if os.IsNotExist(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		var cursor struct {
			Seq int `json:"last_seq"`
		}
		err = json.Unmarshal(raw, &cursor)
		return cursor.Seq == seq, err
	})
}

func readLines[T any](path string, result *[]T) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	for {
		line, rest, complete := bytes.Cut(data, []byte("\n"))
		if !complete {
			return nil
		}
		var value T
		if err := json.Unmarshal(line, &value); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		*result = append(*result, value)
		data = rest
	}
}

func waitFor(ctx context.Context, description string, ready func(context.Context) (bool, error)) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		if ok, err := ready(ctx); err != nil || ok {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for %s: %w", description, ctx.Err())
		case <-tick.C:
		}
	}
}

func child(ctx context.Context, dir string, env []string, bin string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir, cmd.Env = dir, env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

func runCommand(ctx context.Context, dir string, env []string, bin string, input []byte, args ...string) ([]byte, error) {
	cmd := child(ctx, dir, env, bin, args...)
	cmd.Stdin = bytes.NewReader(input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w: %s", filepath.Base(bin), strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

type process struct {
	cancel func()
	signal func(os.Signal) error
	done   chan struct{}
	log    string
	err    error
}

func startProcess(parent context.Context, dir string, env []string, bin, log string, args ...string) (*process, error) {
	stdout, err := os.OpenFile(log+".jsonl", os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	defer stdout.Close()
	stderr, err := os.OpenFile(log+".err", os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	defer stderr.Close()
	ctx, cancel := context.WithCancel(parent)
	cmd := child(ctx, dir, env, bin, args...)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	p := &process{cancel: cancel, signal: cmd.Process.Signal, done: make(chan struct{}), log: stderr.Name()}
	go func() {
		p.err = cmd.Wait()
		close(p.done)
	}()
	return p, nil
}

func (p *process) running() error {
	select {
	case <-p.done:
		data, _ := os.ReadFile(p.log)
		return fmt.Errorf("process exited (%v), see %s: %s", p.err, p.log, strings.TrimSpace(string(data)))
	default:
		return nil
	}
}

func (p *process) stop() {
	if p != nil {
		p.cancel()
		<-p.done
	}
}
