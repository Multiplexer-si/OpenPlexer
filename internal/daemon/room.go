package daemon

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/multiplexer-si/openplexer/internal/room"
)

const (
	roomCursorFile = "room-cursor.json"
	roomStreamType = "application/x-ndjson"
)

// RoomOutboxInterval is the time between attempts to flush queued Room posts.
const RoomOutboxInterval = time.Second

type roomCursor struct {
	LastSeq int64 `json:"last_seq"`
}

var errRoomBehind = errors.New("hub reported behind")

// Run keeps policy fresh and delivers Room events until ctx is canceled.
// A local delivery or cursor write failure stops the process. An unavailable
// hub leaves the policy and cursor on disk for the next attempt.
func Run(ctx context.Context, home string, now func() time.Time, receive func(room.Event) error, report func(error)) error {
	cfg, err := readConfig(home)
	if err != nil {
		return err
	}
	if _, err := cfg.client(); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(home, "room.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := tryLockFileExclusive(file); err != nil {
		return fmt.Errorf("cannot lock the Room cursor: %w", err)
	}
	var cursor roomCursor
	found, err := readJSON(home, roomCursorFile, map[string]any{"last_seq": &cursor.LastSeq})
	if err != nil {
		return err
	}
	if found && (cursor.LastSeq <= 0 || cursor.LastSeq > 1<<53-1) {
		return errors.New("invalid stored Room cursor")
	}

	ctx, cancel := context.WithCancel(ctx)
	policyDone := make(chan struct{})
	go func() {
		defer close(policyDone)
		pollHub(ctx, home, now, report)
	}()
	defer func() {
		cancel()
		<-policyDone
	}()

	receiver := room.NewReceiver()
	receiver.Restore(cfg.ProjectID, cursor.LastSeq)
	for ctx.Err() == nil {
		cfg, err = readConfig(home)
		if err != nil {
			return err
		}
		client, err := cfg.client()
		if err != nil {
			return err
		}
		err = client.followRoom(ctx, now, receiver, func(step room.Step, seq int64) error {
			if delivered, ok := step.(room.Deliver); ok {
				if err := receive(delivered.Event); err != nil {
					return err
				}
			}
			return writeJSON(home, roomCursorFile, roomCursor{LastSeq: seq})
		})
		var local *deliveryError
		if errors.As(err, &local) {
			return local
		}
		if ctx.Err() != nil {
			break
		}
		if err != io.EOF {
			report(fmt.Errorf("Room stream: %w", err))
		}
		if errors.Is(err, errRoomBehind) {
			continue
		}
		select {
		case <-time.After(time.Second):
		case <-ctx.Done():
		}
	}
	return nil
}

func pollHub(ctx context.Context, home string, now func() time.Time, report func(error)) {
	policies := time.NewTicker(15 * time.Second)
	posts := time.NewTicker(RoomOutboxInterval)
	defer policies.Stop()
	defer posts.Stop()
	syncPolicy := func() {
		gate, err := Open(home, now)
		if err == nil {
			_, err = gate.Sync(ctx)
		}
		if err != nil && ctx.Err() == nil {
			report(fmt.Errorf("policy sync: %w", err))
		}
	}
	syncPolicy()
	for ctx.Err() == nil {
		select {
		case <-policies.C:
			syncPolicy()
		case <-posts.C:
			if err := flushRoom(ctx, home, report); err != nil && ctx.Err() == nil {
				report(fmt.Errorf("Room posts: %w", err))
			}
		case <-ctx.Done():
			return
		}
	}
}

type deliveryError struct{ error }

func (h hubClient) followRoom(ctx context.Context, now func() time.Time, receiver *room.Receiver, save func(room.Step, int64) error) (err error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	const idleTimeout = 10 * time.Second
	idle := time.AfterFunc(idleTimeout, func() { cancel(context.DeadlineExceeded) })
	defer idle.Stop()

	path := h.roomPath("stream") + "?last_seq=" + strconv.FormatInt(receiver.LastSeq(h.project), 10)
	response, err := h.request(ctx, http.MethodGet, path, nil, "", "")
	if err != nil {
		return err
	}
	defer func() {
		cancel(nil)
		response.Body.Close()
	}()
	if response.Header.Get("Content-Type") != roomStreamType {
		return errors.New("hub response is not a Room stream")
	}

	var discarded uint64
	defer func() {
		if discarded > 0 && err != nil {
			err = fmt.Errorf("discarded %d Room frames outside the subscription: %w", discarded, err)
		}
	}()
	scanner := bufio.NewScanner(response.Body)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		idle.Reset(idleTimeout)
		frame := scanner.Bytes()
		if len(frame) == 0 || !jsontext.Value(frame).IsValid() {
			continue
		}
		before := receiver.LastSeq(h.project)
		step := receiver.ReceiveInRoom(frame, now(), h.project)
		if _, outside := step.(room.OutsideSubscription); outside {
			discarded++
			continue
		}
		after := receiver.LastSeq(h.project)
		if after > before {
			if err := save(step, after); err != nil {
				return &deliveryError{err}
			}
		}
		switch step := step.(type) {
		case room.SendResume:
			return errRoomBehind
		case room.Unhandled:
			if payload, ok := step.Message.Payload().(room.Error); ok && payload.Code.CloseCode() != 0 {
				return fmt.Errorf("hub reported %s", payload.Code)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return hubIOError{"could not read the Room stream", err}
	}
	return io.EOF
}

func (h hubClient) roomPath(endpoint string) string {
	return "/api/v1/rooms/" + h.project + "/" + endpoint
}
