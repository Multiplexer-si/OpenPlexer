package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/multiplexer-si/openplexer/internal/daemon"
	"github.com/multiplexer-si/openplexer/internal/room"
)

func (c command) daemon() int {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	encoder := json.NewEncoder(c.stdout)
	var log sync.Mutex
	err := daemon.Run(ctx, c.home, c.now, func(event room.Event) error { return encoder.Encode(event) }, func(err error) {
		log.Lock()
		defer log.Unlock()
		fmt.Fprintf(c.stderr, "openplexer: %v\n", err)
	})
	if err != nil {
		return c.fail(err)
	}
	return exitOK
}
