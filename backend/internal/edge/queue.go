package edge

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/updrules"
)

// queue keeps payments and service updates that came while the core was
// down: one append-only file per bot, every record fsynced and checked by
// CRC. A payment cannot be thrown away, so there is no expiry.
//
//	record: length uint32 | crc32 uint32 | update JSON
type queue struct {
	mu  sync.Mutex
	dir string
}

// MethodDeferred is the core method that takes the edge's queue.
const MethodDeferred = "ingress.deferred"

// Deferred is a batch of queued updates of one bot, oldest first.
type Deferred struct {
	BotID   int64    `cbor:"1,keyasint"`
	Updates [][]byte `cbor:"2,keyasint"`
}

func (q *queue) path(botID int64) string {
	return filepath.Join(q.dir, strconv.FormatInt(botID, 10)+".q")
}

// Add appends an update and syncs it to disk.
func (q *queue) Add(botID int64, update []byte) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := os.MkdirAll(q.dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(q.path(botID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:4], uint32(len(update)))
	binary.BigEndian.PutUint32(hdr[4:], crc32.ChecksumIEEE(update))
	if _, err := f.Write(append(hdr[:], update...)); err != nil {
		return err
	}
	return f.Sync()
}

// read returns the complete records of a file; a record cut by a crash or
// damaged on disk ends the reading.
func read(path string) ([][]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out [][]byte
	r := bytes.NewReader(b)
	for {
		var hdr [8]byte
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return out, nil
		}
		n := binary.BigEndian.Uint32(hdr[:4])
		if int64(n) > int64(r.Len()) {
			return out, nil // cut in the middle of a write
		}
		data := make([]byte, n)
		io.ReadFull(r, data)
		if crc32.ChecksumIEEE(data) != binary.BigEndian.Uint32(hdr[4:]) {
			return out, errors.New("edge: damaged record in " + filepath.Base(path))
		}
		out = append(out, data)
	}
}

// Bots lists bots with queued updates.
func (q *queue) Bots() []int64 {
	ents, _ := os.ReadDir(q.dir)
	var ids []int64
	for _, e := range ents {
		if id, err := strconv.ParseInt(strings.TrimSuffix(e.Name(), ".q"), 10, 64); err == nil && strings.HasSuffix(e.Name(), ".q") {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// Len returns how many updates wait for a bot.
func (q *queue) Len(botID int64) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	recs, _ := read(q.path(botID))
	return len(recs)
}

// Drain hands every queue to send in order and deletes a file only after
// send succeeded. Updates added meanwhile stay for the next drain.
func (q *queue) Drain(send func(Deferred) error) error {
	for _, id := range q.Bots() {
		q.mu.Lock()
		p := q.path(id)
		recs, err := read(p)
		if err == nil && len(recs) > 0 {
			// Move the file aside so new updates start a new one.
			tmp := p + ".sending"
			err = os.Rename(p, tmp)
			q.mu.Unlock()
			if err != nil {
				return err
			}
			if err := send(Deferred{BotID: id, Updates: recs}); err != nil {
				// Put it back in front of anything that came meanwhile.
				q.mu.Lock()
				newer, _ := read(p)
				os.Rename(tmp, p)
				for _, u := range newer {
					q.appendLocked(id, u)
				}
				q.mu.Unlock()
				return err
			}
			os.Remove(tmp)
			continue
		}
		q.mu.Unlock()
		if err != nil {
			return err
		}
		os.Remove(p)
	}
	return nil
}

func (q *queue) appendLocked(botID int64, u []byte) {
	f, err := os.OpenFile(q.path(botID), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[:4], uint32(len(u)))
	binary.BigEndian.PutUint32(hdr[4:], crc32.ChecksumIEEE(u))
	f.Write(append(hdr[:], u...))
	f.Sync()
	f.Close()
}

// recover finishes a drain interrupted by a crash: a ".sending" file was
// not confirmed, so it goes back in front.
func (q *queue) recover() {
	ents, _ := os.ReadDir(q.dir)
	for _, e := range ents {
		name := e.Name()
		if !strings.HasSuffix(name, ".q.sending") {
			continue
		}
		sending := filepath.Join(q.dir, name)
		main := strings.TrimSuffix(sending, ".sending")
		newer, _ := read(main)
		os.Rename(sending, main)
		id, _ := strconv.ParseInt(strings.TrimSuffix(name, ".q.sending"), 10, 64)
		for _, u := range newer {
			q.appendLocked(id, u)
		}
	}
}

// withoutCore answers by the maintenance rules when the core does not take
// the update: users get the maintenance text (or "bot unavailable"),
// payments and service updates wait in the queue.
func (e *Edge) withoutCore(u Update) Reply {
	bs, _ := e.settings.bot(u.BotID)
	text := bs.MaintenanceText
	if text == "" {
		text = bs.UnavailableText
	}
	info := updrules.Parse(u.Body)
	d := updrules.Decide(info, updrules.Maintenance, text, e.replies)
	e.count("without_core")
	switch d.Action {
	case updrules.Reply:
		return Reply{Status: 200, ContentType: "application/json", Body: d.Body}
	case updrules.Defer:
		if err := e.queue.Add(u.BotID, u.Body); err != nil {
			e.Log.Error("queueing update failed", "bot", u.BotID, "err", err)
			// Telegram retries a failed webhook: better than losing a payment.
			return Reply{Status: 500}
		}
		e.count("queued")
	}
	return Reply{Status: 200}
}

// drainToCore hands the queue to the core after it connects.
func (e *Edge) drainToCore(c interface {
	Call(context.Context, string, any, any) error
}) {
	err := e.queue.Drain(func(d Deferred) error {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		return c.Call(ctx, MethodDeferred, d, nil)
	})
	if err != nil {
		e.Log.Warn("queue not handed to core yet", "err", err)
	}
}
