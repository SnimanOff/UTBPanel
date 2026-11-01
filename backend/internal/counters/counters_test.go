package counters

import (
	"testing"
	"time"

	"github.com/axiomhq/hyperloglog"

	"github.com/SnimanOff/UTBPanel/backend/internal/ingress"
	"github.com/SnimanOff/UTBPanel/backend/internal/metrics"
)

func TestMinutesAndAck(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 10, 0, time.UTC)
	c := New()
	c.Now = func() time.Time { return now }
	msg := `{"update_id":1,"message":{"chat":{"id":5,"type":"private"},"from":{"id":5,"language_code":"ru"},"text":"hi"}}`
	for i := range 10 {
		c.Observe(ingress.Outcome{Update: ingress.Parse(1, []byte(msg), now), Result: "delivered", Latency: time.Duration(i+1) * 10 * time.Millisecond})
	}
	c.Observe(ingress.Outcome{Update: ingress.Parse(2, []byte(msg), now), Result: "dropped_ban"})
	c.NewUser(1, 5, "ads")
	c.Payment(1, ingress.Payment{Currency: "xtr", Amount: 50})
	c.Gauge(1, metrics.RSS, 100)
	c.Gauge(1, metrics.RSS, 50)

	if got := c.Since(0, 100); len(got) != 0 {
		t.Fatal("unfinished minute exported")
	}
	now = now.Add(time.Minute)
	got := c.Since(0, 100)
	if len(got) != 3 { // bot 1, bot 2 and the sum
		t.Fatalf("minutes = %d", len(got))
	}
	var b1, sum metrics.Minute
	for _, m := range got {
		switch m.BotID {
		case 1:
			b1 = m
		case 0:
			sum = m
		}
	}
	if b1.Counters[metrics.Updates] != 10 || b1.Counters["type_message"] != 10 || b1.Counters["chat_private"] != 10 ||
		b1.Counters["src_ads"] != 1 || b1.Counters["pay_XTR_sum"] != 50 || b1.Gauges[metrics.RSS] != 100 {
		t.Fatalf("bot 1 = %+v", b1)
	}
	if p := b1.Hists[metrics.RespTime].Percentile(0.5); p < 40 || p > 60 {
		t.Fatalf("p50 = %v", p)
	}
	if sum.Counters[metrics.Updates] != 11 || sum.Counters["drop_ban"] != 1 {
		t.Fatalf("sum = %+v", sum.Counters)
	}
	var h hyperloglog.Sketch
	if err := h.UnmarshalBinary(sum.Uniq[metrics.UniqUsers]); err != nil || h.Estimate() != 1 {
		t.Fatalf("unique users = %d %v", h.Estimate(), err)
	}
	// Not confirmed: fetched again. Confirmed: gone.
	if len(c.Since(0, 100)) != 3 {
		t.Fatal("minutes deleted before ack")
	}
	c.Ack(got[len(got)-1].ID)
	if c.Pending() != 0 {
		t.Fatal("ack did not delete")
	}
	// Older than Keep without a panel: dropped.
	c.Add(1, "x", 1)
	now = now.Add(7 * time.Hour)
	if len(c.Since(0, 100)) != 0 {
		t.Fatal("minutes older than 6 h kept")
	}
}

func TestPercentile(t *testing.T) {
	h := metrics.NewHist()
	for i := 1; i <= 100; i++ {
		h.Add(time.Duration(i) * time.Millisecond)
	}
	if p := h.Percentile(0.95); p < 90 || p > 100 {
		t.Fatalf("p95 = %v", p)
	}
	h2 := metrics.NewHist()
	h2.Add(5 * time.Second)
	if h.Merge(h2).Count() != 101 {
		t.Fatal("merge")
	}
}
