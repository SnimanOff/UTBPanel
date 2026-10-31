package edge

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestQueueOrderAndConfirm(t *testing.T) {
	q := &queue{dir: t.TempDir()}
	for i := range 5 {
		q.Add(1, []byte(fmt.Sprintf(`{"update_id":%d}`, i)))
	}
	q.Add(2, []byte(`{"update_id":100}`))
	// The core fails: nothing is lost.
	if err := q.Drain(func(Deferred) error { return errors.New("down") }); err == nil {
		t.Fatal("want error")
	}
	if q.Len(1) != 5 {
		t.Fatalf("after failed drain: %d", q.Len(1))
	}
	var got []string
	err := q.Drain(func(d Deferred) error {
		for _, u := range d.Updates {
			got = append(got, fmt.Sprintf("%d:%s", d.BotID, u))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `1:{"update_id":0} 1:{"update_id":1} 1:{"update_id":2} 1:{"update_id":3} 1:{"update_id":4} 2:{"update_id":100}`
	if strings.Join(got, " ") != want {
		t.Fatalf("order:\n%s", strings.Join(got, " "))
	}
	if len(q.Bots()) != 0 {
		t.Fatal("files left after confirmation")
	}
}

func TestQueueCutRecord(t *testing.T) {
	q := &queue{dir: t.TempDir()}
	q.Add(1, []byte(`{"update_id":1}`))
	q.Add(1, []byte(`{"update_id":2}`))
	// A crash in the middle of the third write.
	f, _ := os.OpenFile(q.path(1), os.O_APPEND|os.O_WRONLY, 0)
	f.Write([]byte{0, 0, 0, 50, 1, 2, 3, 4, '{', '"'})
	f.Close()
	if n := q.Len(1); n != 2 {
		t.Fatalf("records = %d", n)
	}
}

func TestQueueRecoverSending(t *testing.T) {
	dir := t.TempDir()
	q := &queue{dir: dir}
	q.Add(1, []byte(`a`))
	os.Rename(q.path(1), q.path(1)+".sending") // crash after taking the file
	q.Add(1, []byte(`b`))
	q.recover()
	recs, _ := read(q.path(1))
	if len(recs) != 2 || string(recs[0]) != "a" || string(recs[1]) != "b" {
		t.Fatalf("records = %q", recs)
	}
}

func TestWithoutCore(t *testing.T) {
	e, _ := newEdge(t)
	e.settings.apply(Settings{Bots: map[int64]BotSettings{7: {Secret: "s3cret", UnavailableText: "Бот перезапускается"}}})
	msg := `{"update_id":1,"message":{"chat":{"id":5,"type":"private"},"from":{"id":5},"text":"hi"}}`
	// No core connected.
	w := post(e, "/hook/7", telegramIP, "s3cret", msg)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "Бот перезапускается") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	pay := `{"update_id":2,"message":{"chat":{"id":5,"type":"private"},"from":{"id":5},"successful_payment":{"total_amount":1}}}`
	if w := post(e, "/hook/7", telegramIP, "s3cret", pay); w.Code != 200 || w.Body.Len() != 0 {
		t.Fatalf("payment: %d %s", w.Code, w.Body)
	}
	if e.queue.Len(7) != 1 {
		t.Fatal("payment not queued")
	}
	// Saved settings survive a restart of the edge.
	e2, err := New(e.Cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if s, ok := e2.settings.secret(7); !ok || s != "s3cret" {
		t.Fatal("settings not loaded from disk")
	}
}
