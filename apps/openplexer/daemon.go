package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/multiplexer-si/openplexer/internal/daemon"
	"github.com/multiplexer-si/openplexer/internal/room"
)

func (c command) daemon() int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	encoder := json.NewEncoder(c.stdout)
	var log sync.Mutex
	err := daemon.Run(ctx, c.home, c.now, func(event room.Event) error {
		if err := encoder.Encode(event); err != nil {
			return err
		}
		log.Lock()
		defer log.Unlock()
		reportQueuedDenial(c.stderr, event)
		return nil
	}, func(err error) {
		log.Lock()
		defer log.Unlock()
		fmt.Fprintf(c.stderr, "openplexer: %v\n", err)
	})
	if err != nil {
		return c.fail(err)
	}
	return exitOK
}

func reportQueuedDenial(out io.Writer, event room.Event) {
	if event.Type() != "policy.denied" {
		return
	}
	denial := event.Body().(*room.PolicyDenied)
	decided, err := time.Parse(room.TimestampLayout, denial.DecidedAt)
	if err != nil {
		return
	}
	received, err := time.Parse(time.RFC3339Nano, event.At())
	if err == nil && received.Sub(decided).Abs() > daemon.RoomOutboxInterval {
		fmt.Fprintf(out, "openplexer: Room %q denial %q was queued (decided at %s, received at %s)\n",
			event.Mission(), event.ID(), denial.DecidedAt, event.At())
	}
}

func (c command) post() int {
	text, err := io.ReadAll(io.LimitReader(c.stdin, (64<<10)+1))
	if err == nil {
		err = daemon.QueuePost(c.home, text)
	}
	if err != nil {
		return c.fail(err)
	}
	return exitOK
}
