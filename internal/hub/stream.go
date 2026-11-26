package hub

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/multiplexer-si/openplexer/internal/room"
)

const roomStreamType = "application/x-ndjson"

func (s *Store) streamRoom(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("room")
	hash, err := bearerHash(r)
	if err != nil {
		respond(w, nil, err)
		return
	}
	read := func(after int64, page *[]room.Message) error {
		return s.view(func(st *state) error {
			if _, _, err := st.authenticate(hash); err != nil {
				return err
			}
			if _, ok := st.Projects[roomID]; !ok {
				return errNotFound
			}
			if page != nil {
				var err error
				*page, err = roomPage(st.Rooms[roomID], uint64(after))
				return err
			}
			return nil
		})
	}
	if err := read(0, nil); err != nil {
		respond(w, nil, err)
		return
	}
	download := r.Method != http.MethodPost
	var cursor int64
	controller := http.NewResponseController(w)
	if download {
		after, err := replayCursor(r.URL.RawQuery)
		if err != nil {
			respond(w, nil, err)
			return
		}
		cursor = int64(after)
	} else {
		if r.ProtoMajor == 1 {
			if err := controller.EnableFullDuplex(); err != nil {
				writeError(w, http.StatusInternalServerError, "full-duplex HTTP is unavailable")
				return
			}
			// The exit deadline can cancel net/http's connection context after body EOF.
			// Close the connection so the next request gets a fresh context.
			w.Header().Set("Connection", "close")
		}
		if err := controller.SetReadDeadline(time.Time{}); err != nil {
			writeError(w, http.StatusInternalServerError, "stream read deadline is unavailable")
			return
		}
		defer controller.SetReadDeadline(time.Now())
	}

	w.Header().Set("Content-Type", roomStreamType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	if r.Method == http.MethodHead {
		w.WriteHeader(http.StatusOK)
		return
	}
	encoder := json.NewEncoder(w)
	started := false
	send := func(message *room.Message) error {
		err := controller.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err == nil {
			if message == nil {
				_, err = w.Write([]byte("\n"))
			} else {
				err = encoder.Encode(message)
			}
		}
		if err == nil {
			err = controller.Flush()
		}
		if err != nil && download {
			panic(http.ErrAbortHandler)
		}
		started = true
		return err
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	var resumes chan room.Resume
	if !download {
		if err := send(nil); err != nil {
			return
		}
		resumes = make(chan room.Resume)
		go readResumes(ctx, r, resumes)
	}
	ticks := time.NewTicker(time.Second)
	defer ticks.Stop()

	var page []room.Message
	subscribed, readNow := download, download
	replace := func(resume room.Resume) {
		cursor, page = resume.LastSeq, nil
		subscribed, readNow = true, true
	}
	for {
		// A new resume discards the untransmitted part of the previous replay.
		select {
		case resume, ok := <-resumes:
			if !ok {
				return
			}
			replace(resume)
		case <-ctx.Done():
			return
		default:
		}
		switch {
		case len(page) > 0:
			message := page[0]
			page = page[1:]
			if err := send(&message); err != nil {
				return
			}
			cursor, _ = message.Seq()
		case readNow:
			if err := read(cursor, &page); err != nil {
				if download && !started {
					respond(w, nil, err)
				} else if errors.Is(err, errUnauthorized) {
					message := room.ErrorMessage(room.Sender{ID: "hub", Kind: room.Server}, roomID,
						s.now().UTC().Format(timestampFmt), room.Error{Code: "unauthorized", Message: "invalid bearer token"})
					_ = send(&message)
				} else if download {
					panic(http.ErrAbortHandler)
				}
				return
			}
			if download {
				readNow = len(page) > 0
				if !readNow && send(nil) != nil {
					return
				}
			} else {
				readNow = len(page) == replayPage
			}
		default:
			select {
			case <-ctx.Done():
				return
			case resume, ok := <-resumes:
				if !ok {
					return
				}
				replace(resume)
			case <-ticks.C:
				if !download && send(nil) != nil {
					return
				}
				readNow = subscribed
			}
		}
	}
}

func readResumes(ctx context.Context, r *http.Request, resumes chan<- room.Resume) {
	defer close(resumes)
	scanner := bufio.NewScanner(r.Body)
	scanner.Buffer(make([]byte, 4096), maxRoomBody)
	for scanner.Scan() {
		message, err := room.ParseMessage(scanner.Bytes())
		if err != nil || message.V() != 1 || message.Room() != r.PathValue("room") {
			continue
		}
		if resume, ok := message.Payload().(room.Resume); ok {
			select {
			case resumes <- resume:
			case <-ctx.Done():
				return
			}
		}
	}
}
