package core

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/deploy"
	"github.com/SnimanOff/UTBPanel/backend/internal/events"
	"github.com/SnimanOff/UTBPanel/backend/internal/ingress"
	"github.com/SnimanOff/UTBPanel/backend/internal/journal"
	"github.com/SnimanOff/UTBPanel/backend/internal/store"
)

var rollbackActor = journal.Actor{Source: journal.SourceAutorollback, Name: "autorollback"}

// observation is the minute after a deploy.
type observation struct {
	deployID, botID int64
	from, to        string
	backup          string
	schemaBefore    int64
	start           time.Time
	restarts0       int

	mu      sync.Mutex
	ok, bad int
	result  string        // "ok", "rolled_back" or "failed" (autorollback off)
	done    chan struct{} // closed when result is set
}

type watches struct {
	mu   sync.Mutex
	bots map[int64]*observation
	ids  map[int64]*observation // by deploy id
}

// observeOutcome counts updates of bots under observation.
func (c *Core) observeOutcome(o ingress.Outcome) {
	c.watch.mu.Lock()
	ob := c.watch.bots[o.Update.BotID]
	c.watch.mu.Unlock()
	if ob == nil {
		return
	}
	ob.mu.Lock()
	switch o.Result {
	case "delivered", "replied":
		ob.ok++
	case "error", "timeout":
		ob.bad++
	}
	ob.mu.Unlock()
}

// observe watches a fresh deploy and rolls it back when it goes bad.
func (c *Core) observe(ob *observation) {
	defer func() {
		c.watch.mu.Lock()
		if c.watch.bots[ob.botID] == ob {
			delete(c.watch.bots, ob.botID)
		}
		c.watch.mu.Unlock()
		// Waiters may come late: keep the result for a while.
		time.AfterFunc(10*time.Minute, func() {
			c.watch.mu.Lock()
			delete(c.watch.ids, ob.deployID)
			c.watch.mu.Unlock()
		})
	}()
	rules, autorollback := c.deployRules(context.Background(), ob.botID)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for range t.C {
		st := c.Runner.Status(ob.botID)
		ob.mu.Lock()
		s := deploy.Stats{Elapsed: time.Since(ob.start), Crashes: st.Restarts - ob.restarts0, Running: st.Running, OK: ob.ok, Bad: ob.bad}
		ob.mu.Unlock()
		// A version switched by hand during the minute ends the watch.
		if cur, _, _ := c.versions(context.Background(), ob.botID); cur != ob.to {
			ob.result = "ok"
			close(ob.done)
			return
		}
		verdict, reason := rules.Judge(s)
		switch verdict {
		case deploy.Passed:
			c.DB.W.Exec(`UPDATE deploys SET result = 'ok', finished_at = ? WHERE id = ?`, store.Now(), ob.deployID)
			c.Logs.Add(ob.botID, "core", "version "+ob.to+" passed the observation", nowUTC())
			c.Ev.Emit("deploy_passed", ob.botID, events.Info, map[string]any{"id": ob.deployID, "version": ob.to})
			ob.result = "ok"
			close(ob.done)
			return
		case deploy.Rollback:
			if !autorollback {
				// The bot turned autorollback off: report and leave the version.
				msg := reason + "; авто-откат выключен"
				c.DB.W.Exec(`UPDATE deploys SET result = 'failed', error = ?, finished_at = ? WHERE id = ?`, msg, store.Now(), ob.deployID)
				c.Logs.Add(ob.botID, "core", "version "+ob.to+" failed the observation: "+msg, nowUTC())
				c.Ev.Emit("deploy", ob.botID, events.Error, map[string]any{"id": ob.deployID, "from": ob.from, "version": ob.to, "result": "failed", "error": msg})
				ob.result = "failed"
				close(ob.done)
				return
			}
			c.rollback(context.Background(), ob, reason)
			ob.result = "rolled_back"
			close(ob.done)
			return
		}
	}
}

// rollback puts the previous version back; the database backup is
// restored only if the new version changed the schema.
func (c *Core) rollback(ctx context.Context, ob *observation, reason string) {
	restored := false
	var errs []string
	err := c.Journal.Do(ctx, rollbackActor, "releases.rollback", ob.botID, map[string]string{"from": ob.to, "to": ob.from, "reason": reason}, func() error {
		if c.Runner.IsRunning(ob.botID) {
			if err := c.Runner.Stop(ctx, ob.botID); err != nil {
				return err
			}
		}
		if _, err := c.DB.W.ExecContext(ctx, `UPDATE bots SET current_version = ?, previous_version = ? WHERE id = ?`, ob.from, ob.to, ob.botID); err != nil {
			return err
		}
		if ob.backup != "" {
			if now, err := c.DBG.SchemaVersion(ctx, ob.botID); err == nil && now != ob.schemaBefore {
				if err := c.RestoreDB(ctx, ob.botID, ob.backup); err != nil {
					errs = append(errs, "восстановление базы: "+err.Error())
				} else {
					restored = true
				}
			}
		}
		if ob.from != "" {
			if err := c.StartBot(ctx, ob.botID); err != nil {
				errs = append(errs, "запуск "+ob.from+": "+err.Error())
			}
		}
		return nil
	})
	if err != nil {
		errs = append(errs, err.Error())
	}
	msg := reason
	for _, e := range errs {
		msg += "; " + e
	}
	c.DB.W.ExecContext(ctx, `UPDATE deploys SET result = 'rolled_back', error = ?, finished_at = ? WHERE id = ?`, msg, store.Now(), ob.deployID)
	c.Logs.Add(ob.botID, "core", fmt.Sprintf("autorollback %s → %s: %s", ob.to, ob.from, msg), nowUTC())
	c.Ev.Emit("autorollback", ob.botID, events.Error, map[string]any{
		"id": ob.deployID, "from": ob.to, "to": ob.from, "reason": reason, "errors": errs,
		"db_restored": restored, "payments": c.paymentsSince(ctx, ob.botID, ob.start),
	})
	c.Ev.Emit("deploy", ob.botID, events.Error, map[string]any{"id": ob.deployID, "from": ob.to, "version": ob.from, "result": "rolled_back", "error": msg})
}

// paymentsSince lists payments made while the bad version ran: the owner
// checks them by hand.
func (c *Core) paymentsSince(ctx context.Context, botID int64, since time.Time) []map[string]any {
	rows, err := c.DB.R.QueryContext(ctx, `SELECT ts, user_id, currency, amount, charge_id FROM payments WHERE bot_id = ? AND ts >= ? ORDER BY ts LIMIT 100`,
		botID, store.FormatTime(since))
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var ts, cur, charge string
		var user, amount int64
		if rows.Scan(&ts, &user, &cur, &amount, &charge) == nil {
			out = append(out, map[string]any{"ts": ts, "user_id": user, "currency": cur, "amount": amount, "charge_id": charge})
		}
	}
	return out
}

// waitDeploy waits for the observation of a deploy: "ok", "rolled_back" or "failed".
func (c *Core) waitDeploy(ctx context.Context, deployID int64) string {
	c.watch.mu.Lock()
	ob := c.watch.ids[deployID]
	c.watch.mu.Unlock()
	if ob == nil {
		return "ok" // nothing was watched (the bot is stopped)
	}
	select {
	case <-ob.done:
		return ob.result
	case <-ctx.Done():
		return "ok"
	}
}

// watchDeploy starts the observation of a deploy that started the bot.
func (c *Core) watchDeploy(id, botID int64, from, to, backup string, schema int64) {
	ob := &observation{deployID: id, botID: botID, from: from, to: to, backup: backup, schemaBefore: schema,
		start: time.Now(), restarts0: c.Runner.Status(botID).Restarts, done: make(chan struct{})}
	c.DB.W.Exec(`UPDATE deploys SET result = 'observing' WHERE id = ?`, id)
	// Registered before the goroutine: waitDeploy may come right away.
	c.watch.mu.Lock()
	if c.watch.bots == nil {
		c.watch.bots, c.watch.ids = map[int64]*observation{}, map[int64]*observation{}
	}
	c.watch.bots[botID], c.watch.ids[id] = ob, ob
	c.watch.mu.Unlock()
	go c.observe(ob)
}

// rollbackNow is for a new version that did not even start.
func (c *Core) rollbackNow(id, botID int64, from, to, backup string, schema int64, reason string) {
	ob := &observation{deployID: id, botID: botID, from: from, to: to, backup: backup, schemaBefore: schema,
		start: time.Now(), done: make(chan struct{})}
	c.rollback(context.Background(), ob, reason)
	ob.result = "rolled_back"
	close(ob.done)
}
