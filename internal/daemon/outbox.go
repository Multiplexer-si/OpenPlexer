package daemon

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/multiplexer-si/openplexer/internal/policy"
	"github.com/multiplexer-si/openplexer/internal/room"
)

// QueuePost validates and stores a Room post for the next connected daemon.
func QueuePost(home string, text []byte) error {
	cfg, err := readConfig(home)
	if err != nil {
		return err
	}
	if _, err := cfg.client(); err != nil {
		return err
	}
	return enqueue(home, text, false)
}

func enqueue(home string, text []byte, denial bool) error {
	post, err := preparePost(text, denial)
	if err != nil {
		return err
	}
	name := fmt.Sprintf("%020d-%s.json", time.Now().UnixNano(), rand.Text())
	return writeFile(filepath.Join(home, "room-outbox"), name, post)
}

// QueueDenial reports a policy verdict from a hook. The local audit retains
// the full rule. Room metadata uses a fixed rule and a safe relative path.
func (g *Gate) QueueDenial(v Verdict, agent string) error {
	result, decided := v.Result()
	q := v.Query
	if !g.config.hasHubSettings() || !decided || result.Decision != policy.Deny ||
		q.AgentSessionID == nil || !hubID.MatchString(*q.AgentSessionID) {
		return nil
	}
	text, err := json.Marshal(map[string]any{
		"type": "policy.denied", "agent": map[string]string{"id": *q.AgentSessionID, "vendor": agent},
		"body": map[string]string{"action": string(q.Action), "path": q.Path, "rule": "denied by policy"},
	})
	if err != nil {
		return err
	}
	return enqueue(g.home, text, true)
}

func flushRoom(ctx context.Context, home string, report func(error)) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	dir := filepath.Join(home, "room-outbox")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	cfg, err := readConfig(home)
	if err != nil {
		return err
	}
	client, err := cfg.client()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		key, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || entry.Type() != 0 || !hubID.MatchString(key) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		text, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		post, err := preparePost(text, true)
		if err != nil {
			return fmt.Errorf("invalid Room outbox entry %s: %w", key, err)
		}
		if err := client.sendPost(ctx, key, post); err != nil {
			var status hubStatus
			if !errors.As(err, &status) || status != 400 && status != 403 && status != 413 {
				return err
			}
			report(fmt.Errorf("Room rejected post %s: %w", key, err))
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}

func (h hubClient) sendPost(ctx context.Context, key string, post []byte) error {
	response, err := h.request(ctx, http.MethodPost, h.roomPath("events"), bytes.NewReader(post), "application/json", key)
	if err != nil {
		return err
	}
	body, err := readResponse(response, 1<<20)
	if err != nil {
		return err
	}
	message, err := room.ParseMessage(body)
	if err != nil {
		return err
	}
	event, ok := message.Payload().(room.Event)
	if !ok || message.V() != 1 || message.Room() != h.project || event.OnBehalfOf() != h.member {
		return errors.New("hub did not acknowledge the Room post for the configured member")
	}
	return nil
}
