package bots

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/alerts"
	"github.com/SnimanOff/UTBPanel/backend/internal/api"
	"github.com/SnimanOff/UTBPanel/backend/internal/app"
	"github.com/SnimanOff/UTBPanel/backend/internal/core"
	"github.com/SnimanOff/UTBPanel/backend/internal/metrics"
	"github.com/SnimanOff/UTBPanel/backend/internal/panics"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/internal/store/migrations"
	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

func TestOverviewCombinesCoreAndPanelDiagnostics(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "panel.db"), migrations.Panel())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, query := range []string{
		`INSERT INTO groups(id,name) VALUES(1,'test')`,
		`INSERT INTO bot_meta(bot_id,group_id) VALUES(7,1)`,
		`INSERT INTO alert_rules(id,bot_id,metric,value) VALUES(1,0,'cpu',80),(2,7,'rss',10),(3,8,'cpu',80)`,
		`INSERT INTO alerts_log(rule_id,bot_id,ts,state,value,text) VALUES(1,7,1,'fired',90,'active'),(2,7,2,'fired',20,'old'),(2,7,3,'ok',5,'resolved'),(3,8,4,'fired',90,'other bot')`,
		`INSERT INTO deploy_reports(deploy_id,bot_id,ts,version,before,after) VALUES(123,7,1,'v2','{}','{}'),(999,8,2,'other','{}','{}')`,
	} {
		if _, err := db.W.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	panicStore := &panics.Store{DB: db}
	for _, id := range []int64{7, 8} {
		if err := panicStore.Add(ctx, panics.Panic{BotID: id, Time: time.Now(), Kind: "boom", Signature: "boom", Stack: "stack", Version: "v2"}); err != nil {
			t.Fatal(err)
		}
	}
	run, err := os.MkdirTemp("", "overview-wire-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(run)
	path := filepath.Join(run, "c.sock")
	ln, err := wire.Listen(path, 0o600, -1)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	mux := wire.NewMux()
	mux.Handle("bots.overview", wire.Func(func(_ context.Context, _ *wire.Conn, id int64) (core.Overview, error) {
		if id != 7 {
			return core.Overview{}, wire.Errorf(wire.CodeNotFound, "no bot")
		}
		return core.Overview{Bot: core.Bot{ID: 7}, Deferred: 2, LastDeploy: &core.DeployRecord{ID: 123, To: "v2", Result: "ok"}}, nil
	}))
	go (&wire.Server{Mux: mux, Hello: app.Hello(wire.ProtoCorePanel, "core", 1, 1)}).Serve(ln)
	client := &wire.Client{Path: path, Hello: app.Hello(wire.ProtoCorePanel, "panel", 1, 1)}
	go client.Run(ctx)
	conn, err := client.Wait(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	s := &OverviewAPI{Bots: &Service{Core: client, Groups: &Groups{DB: db}}, Metrics: &metrics.Storage{DB: db}, Panics: panicStore, Rules: &alerts.Rules{S: &metrics.Storage{DB: db}}}
	out, err := s.Get(ctx, 7)
	if err != nil {
		t.Fatal(err)
	}
	if out.Deferred != 2 || out.GroupID != 1 || len(out.Panics) != 1 || out.LastReport == nil || out.LastReport.DeployID != 123 || out.ReportPending || len(out.Rules) != 2 || len(out.Alerts) != 1 || out.Alerts[0].Text != "active" {
		t.Fatalf("wrong combined diagnostics: %+v", out)
	}
	routes := api.NewMux()
	s.Register(routes)
	response := httptest.NewRecorder()
	routes.ServeHTTP(response, httptest.NewRequest("GET", "/api/bots/999/overview", nil))
	if response.Code != 404 {
		t.Fatal(response.Code, response.Body.String())
	}
	if _, err := db.W.Exec(`DELETE FROM deploy_reports WHERE bot_id=7`); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	routes.ServeHTTP(response, httptest.NewRequest("GET", "/api/bots/7/overview", nil))
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if !out.ReportPending || out.LastReport != nil {
		t.Fatal("missing report looks completed", out)
	}
}
