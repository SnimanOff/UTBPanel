package sdkhost

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/SnimanOff/UTBPanel/backend/internal/store"
	"github.com/SnimanOff/UTBPanel/backend/sdk/tasks"
	"github.com/SnimanOff/UTBPanel/backend/sdk/wire"
	"github.com/robfig/cron/v3"
)

type TaskRun struct {
	ID           int64   `cbor:"1,keyasint" json:"id"`
	Name         string  `cbor:"2,keyasint" json:"name"`
	Source       string  `cbor:"3,keyasint" json:"source"`
	Started      string  `cbor:"4,keyasint" json:"started"`
	Finished     string  `cbor:"5,keyasint,omitempty" json:"finished,omitempty"`
	Milliseconds float64 `cbor:"6,keyasint" json:"duration_ms"`
	Success      bool    `cbor:"7,keyasint" json:"success"`
	Error        string  `cbor:"8,keyasint" json:"error"`
}
type TasksView struct {
	Tasks []tasks.Definition `cbor:"1,keyasint" json:"tasks"`
	Runs  []TaskRun          `cbor:"2,keyasint" json:"runs"`
}
type taskJob struct {
	botID  int64
	cancel context.CancelFunc
}
type Tasks struct {
	DB       *store.DB
	Host     *Host
	Runnable func(int64) bool
	Timeout  time.Duration
	Redact   func(string) string
	created  time.Time
	mu       sync.Mutex
	cron     *cron.Cron
	entries  map[int64]map[string]cron.EntryID
	running  map[string]taskJob
	blocked  map[int64]bool
	ctx      context.Context
	cancel   context.CancelFunc
	closed   bool
	wg       sync.WaitGroup
}

func NewTasks(db *store.DB, host *Host, runnable func(int64) bool) *Tasks {
	ctx, cancel := context.WithCancel(context.Background())
	return &Tasks{DB: db, Host: host, Runnable: runnable, created: time.Now(), cron: cron.New(cron.WithLocation(time.UTC)), entries: map[int64]map[string]cron.EntryID{}, running: map[string]taskJob{}, blocked: map[int64]bool{}, ctx: ctx, cancel: cancel}
}

func (t *Tasks) Register(mux *wire.Mux, id int64, instance string) {
	mux.Handle("sdk.tasks.register", wire.Func(func(ctx context.Context, _ *wire.Conn, defs []tasks.Definition) (any, error) {
		if instance == "canary" {
			return nil, nil
		}
		return nil, t.RegisterDefinitions(ctx, id, defs)
	}))
}

func (t *Tasks) RegisterDefinitions(ctx context.Context, id int64, defs []tasks.Definition) error {
	if len(defs) > tasks.MaxTasks {
		return fmt.Errorf("sdk: task limit reached")
	}
	seen := map[string]bool{}
	for _, d := range defs {
		if err := tasks.Validate(d.Name, d.Cron); err != nil {
			return err
		}
		if seen[d.Name] {
			return fmt.Errorf("sdk: duplicate task")
		}
		seen[d.Name] = true
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return fmt.Errorf("sdk: tasks are shutting down")
	}
	if err := t.DB.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM sdk_tasks WHERE bot_id=?`, id); err != nil {
			return err
		}
		for _, d := range defs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO sdk_tasks(bot_id,name,cron) VALUES(?,?,?)`, id, d.Name, d.Cron); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	return t.install(id, defs)
}

func (t *Tasks) install(id int64, defs []tasks.Definition) error {
	for _, entry := range t.entries[id] {
		t.cron.Remove(entry)
	}
	t.entries[id] = map[string]cron.EntryID{}
	for _, d := range defs {
		name := d.Name
		entry, err := t.cron.AddFunc(d.Cron, func() { t.Start(t.ctx, id, name, "cron") })
		if err != nil {
			return err
		}
		t.entries[id][name] = entry
	}
	return nil
}

func (t *Tasks) Run(ctx context.Context) error {
	stop := context.AfterFunc(ctx, t.Close)
	defer stop()
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	err := t.load(ctx)
	if err == nil {
		t.cron.Start()
	}
	t.mu.Unlock()
	if err != nil {
		return err
	}
	<-ctx.Done()
	t.Close()
	return nil
}

// Caller holds mu, so a reconnect cannot replace schedules during loading.
func (t *Tasks) load(ctx context.Context) error {
	if _, err := t.DB.W.ExecContext(ctx, `UPDATE sdk_task_runs SET finished_at=?,error='core restarted',success=0 WHERE finished_at IS NULL AND julianday(started_at)<julianday(?)`, time.Now().UTC().Format(time.RFC3339Nano), t.created.UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	rows, err := t.DB.R.QueryContext(ctx, `SELECT bot_id,name,cron FROM sdk_tasks ORDER BY bot_id,name`)
	if err != nil {
		return err
	}
	byBot := map[int64][]tasks.Definition{}
	for rows.Next() {
		var id int64
		var d tasks.Definition
		if err := rows.Scan(&id, &d.Name, &d.Cron); err != nil {
			rows.Close()
			return err
		}
		byBot[id] = append(byBot[id], d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for id, defs := range byBot {
		if err := t.install(id, defs); err != nil {
			return err
		}
	}
	return nil
}

func (t *Tasks) Start(ctx context.Context, id int64, name, source string) (int64, error) {
	if t.Runnable != nil && !t.Runnable(id) {
		return 0, wire.Errorf("conflict", "bot is stopped, paused or not ready")
	}
	var registered int
	if err := t.DB.R.QueryRowContext(ctx, `SELECT COUNT(*) FROM sdk_tasks WHERE bot_id=? AND name=?`, id, name).Scan(&registered); err != nil {
		return 0, err
	}
	if registered == 0 {
		return 0, wire.Errorf(wire.CodeNotFound, "task is not registered")
	}
	key := strconv.FormatInt(id, 10) + ":" + name
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return 0, wire.ErrClosed
	}
	if t.blocked[id] || t.Runnable != nil && !t.Runnable(id) {
		t.mu.Unlock()
		return 0, wire.Errorf("conflict", "bot is stopped, paused or not ready")
	}
	if _, ok := t.running[key]; ok {
		t.mu.Unlock()
		return 0, wire.Errorf("conflict", "task is already running")
	}
	started := time.Now()
	res, err := t.DB.W.ExecContext(ctx, `INSERT INTO sdk_task_runs(bot_id,name,source,started_at) VALUES(?,?,?,?)`, id, name, source, started.UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.mu.Unlock()
		return 0, err
	}
	runID, err := res.LastInsertId()
	if err != nil {
		t.mu.Unlock()
		return 0, err
	}
	timeout := t.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	jobCtx, cancel := context.WithTimeout(t.ctx, timeout)
	t.running[key] = taskJob{botID: id, cancel: cancel}
	t.wg.Add(1)
	t.mu.Unlock()
	go func() {
		defer t.wg.Done()
		defer cancel()
		err := t.Host.Call(jobCtx, id, "", "sdk.tasks.run", tasks.RunRequest{Name: name, ID: runID}, nil)
		finished := time.Now()
		done, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		errorText := ""
		if err != nil {
			errorText = err.Error()
			if t.Redact != nil {
				errorText = t.Redact(errorText)
			}
			if len(errorText) > 4096 {
				errorText = errorText[:4096]
			}
		}
		t.DB.W.ExecContext(done, `UPDATE sdk_task_runs SET finished_at=?,duration_ms=?,success=?,error=? WHERE id=?`, finished.UTC().Format(time.RFC3339Nano), float64(finished.Sub(started))/float64(time.Millisecond), err == nil, errorText, runID)
		t.DB.W.ExecContext(done, `DELETE FROM sdk_task_runs WHERE bot_id=? AND id IN (SELECT id FROM sdk_task_runs WHERE bot_id=? ORDER BY id DESC LIMIT -1 OFFSET 200)`, id, id)
		t.mu.Lock()
		delete(t.running, key)
		t.mu.Unlock()
	}()
	return runID, nil
}

func (t *Tasks) CancelBot(id int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, job := range t.running {
		if job.botID == id {
			job.cancel()
		}
	}
}

func (t *Tasks) SuspendBot(id int64) {
	t.mu.Lock()
	t.blocked[id] = true
	for _, job := range t.running {
		if job.botID == id {
			job.cancel()
		}
	}
	t.mu.Unlock()
}
func (t *Tasks) ResumeBot(id int64) { t.mu.Lock(); delete(t.blocked, id); t.mu.Unlock() }

func (t *Tasks) Close() {
	t.mu.Lock()
	t.closed = true
	t.cancel()
	for _, job := range t.running {
		job.cancel()
	}
	t.mu.Unlock()
	t.cron.Stop()
	t.wg.Wait()
}

func (t *Tasks) List(ctx context.Context, id int64) (TasksView, error) {
	v := TasksView{Tasks: []tasks.Definition{}, Runs: []TaskRun{}}
	rows, err := t.DB.R.QueryContext(ctx, `SELECT name,cron FROM sdk_tasks WHERE bot_id=? ORDER BY name`, id)
	if err != nil {
		return v, err
	}
	for rows.Next() {
		var d tasks.Definition
		if err := rows.Scan(&d.Name, &d.Cron); err != nil {
			rows.Close()
			return v, err
		}
		v.Tasks = append(v.Tasks, d)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return v, err
	}
	rows, err = t.DB.R.QueryContext(ctx, `SELECT id,name,source,started_at,COALESCE(finished_at,''),duration_ms,success,error FROM sdk_task_runs WHERE bot_id=? ORDER BY id DESC LIMIT 30`, id)
	if err != nil {
		return v, err
	}
	defer rows.Close()
	for rows.Next() {
		var r TaskRun
		if err := rows.Scan(&r.ID, &r.Name, &r.Source, &r.Started, &r.Finished, &r.Milliseconds, &r.Success, &r.Error); err != nil {
			return v, err
		}
		v.Runs = append(v.Runs, r)
	}
	return v, rows.Err()
}
