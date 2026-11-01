package core

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/metrics"
	"github.com/SnimanOff/UTBPanel/backend/internal/sdkhost"
	"github.com/SnimanOff/UTBPanel/backend/sdk"
	sdkmetrics "github.com/SnimanOff/UTBPanel/backend/sdk/metrics"
)

func TestSDKMetricsOverSocketAndPersistence(t *testing.T) {
	c := newCore(t)
	ctx := context.Background()
	if _, err := c.DB.W.Exec(`INSERT INTO bots(id,name,port,created_at) VALUES(1,'sdk',20001,'x')`); err != nil {
		t.Fatal(err)
	}
	uid := -1
	if runtime.GOOS == "linux" {
		uid = os.Geteuid()
	}
	if err := c.SDK.Start(1, "", uid); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.SDK.Close() })
	t.Setenv("TGPANEL_SDK", "unix://"+c.SDK.Path(1, ""))
	t.Setenv("TGPANEL_CANARY", "")
	k := sdk.New()
	defer k.Close()
	orders := k.Counter("orders")
	orders.Add(3)
	k.Value("temperature").Set(-2)
	if err := k.FlushMetrics(ctx); err != nil {
		t.Fatal(err)
	}
	list, err := c.SDKMetrics.List(ctx, 1)
	if err != nil || len(list) != 2 || list[0].Counter != 3 || list[1].Value != -2 {
		t.Fatalf("%+v %v", list, err)
	}
	if err := c.SDKMetrics.Select(ctx, 1, []string{"orders"}); err != nil {
		t.Fatal(err)
	}
	if err := c.SDKMetrics.Select(ctx, 1, []string{"missing"}); err == nil {
		t.Fatal("unknown selection accepted")
	}
	list, _ = c.SDKMetrics.List(ctx, 1)
	if !list[0].Selected {
		t.Fatal("failed selection changed current dashboard")
	}
	// A repeated batch stays acknowledged even after recreating the service.
	b := sdkmetrics.Batch{Source: strings.Repeat("a", 32), Sequence: 1, Points: []sdkmetrics.Point{{Name: "orders", Kind: "counter", Delta: 4}}}
	restarted := &sdkhost.Metrics{DB: c.DB, Count: c.Count}
	if err := restarted.Apply(ctx, 1, "", b); err != nil {
		t.Fatal(err)
	}
	if err := c.SDKMetrics.Apply(ctx, 1, "", b); err != nil {
		t.Fatal(err)
	}
	list, _ = c.SDKMetrics.List(ctx, 1)
	if list[0].Counter != 7 {
		t.Fatalf("duplicate delta: %+v", list)
	}
	for i := 0; i < 200; i++ {
		k.Counter(fmt.Sprintf("extra_%d", i)).Add(1)
	}
	if err := k.FlushMetrics(ctx); err != nil {
		t.Fatal(err)
	}
	list, _ = c.SDKMetrics.List(ctx, 1)
	if len(list) != 200 {
		t.Fatalf("metric limit: %d", len(list))
	}
	var events int
	if err := c.DB.R.QueryRow(`SELECT COUNT(*) FROM events WHERE type='sdk_metric_limit'`).Scan(&events); err != nil || events != 1 {
		t.Fatalf("limit event: %d %v", events, err)
	}
}

func TestSDKGaugeHistoryKeepsZeroAndNegativeValues(t *testing.T) {
	m := metrics.Minute{Gauges: map[string]float64{"sdk_depth": -2}}
	m.Merge(&metrics.Minute{Gauges: map[string]float64{"sdk_depth": 0}})
	var decoded metrics.Minute
	if err := metrics.Decode(metrics.Encode(&m), &decoded); err != nil {
		t.Fatal(err)
	}
	if v, ok := decoded.Gauges["sdk_depth"]; !ok || v != 0 {
		t.Fatalf("%v", decoded.Gauges)
	}
	c := newCore(t)
	now := time.Now().Truncate(time.Minute)
	c.Count.Now = func() time.Time { return now }
	c.Count.SetGauge(1, "sdk_depth", -4)
	now = now.Add(time.Minute)
	rows := c.Count.Since(0, 100)
	for _, row := range rows {
		if row.BotID == 1 && row.Gauges["sdk_depth"] == -4 {
			return
		}
	}
	t.Fatal("negative application gauge was lost")
}
