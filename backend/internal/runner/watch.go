package runner

import (
	"bufio"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Usage is what the watcher measured for one bot.
type Usage struct {
	RSS         int64 `cbor:"1,keyasint" json:"rss"`
	OpenFiles   int64 `cbor:"2,keyasint" json:"open_files"`
	Threads     int64 `cbor:"3,keyasint" json:"threads"`
	FilesTotal  int64 `cbor:"4,keyasint" json:"files_total"`
	BiggestFile int64 `cbor:"5,keyasint" json:"biggest_file"`
	CPUTicks    int64 `cbor:"6,keyasint" json:"cpu_ticks"`
}

// ProcReader reads process data; tests replace it.
type ProcReader interface {
	Usage(pid int) (Usage, error)
	DirSize(dir string) (total, biggest int64)
}

// procfs reads /proc.
type procfs struct{}

func (procfs) Usage(pid int) (Usage, error) {
	var u Usage
	base := "/proc/" + strconv.Itoa(pid)
	f, err := os.Open(base + "/status")
	if err != nil {
		return u, err
	}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, _ := strings.Cut(sc.Text(), ":")
		fields := strings.Fields(v)
		if len(fields) == 0 {
			continue
		}
		n, _ := strconv.ParseInt(fields[0], 10, 64)
		switch k {
		case "VmRSS":
			u.RSS = n << 10 // kB
		case "Threads":
			u.Threads = n
		}
	}
	f.Close()
	if ents, err := os.ReadDir(base + "/fd"); err == nil {
		u.OpenFiles = int64(len(ents))
	}
	if b, err := os.ReadFile(base + "/stat"); err == nil {
		s := string(b)
		if i := strings.LastIndexByte(s, ')'); i > 0 {
			if f := strings.Fields(s[i+1:]); len(f) > 12 {
				ut, _ := strconv.ParseInt(f[11], 10, 64)
				st, _ := strconv.ParseInt(f[12], 10, 64)
				u.CPUTicks = ut + st
			}
		}
	}
	return u, nil
}

func (procfs) DirSize(dir string) (total, biggest int64) {
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
			biggest = max(biggest, info.Size())
		}
		return nil
	})
	return
}

// Watch intervals (variables for tests).
var (
	WatchEvery     = 5 * time.Second
	WatchDiskEvery = time.Minute
	warnEvery      = time.Hour
)

// watchState keeps per-bot warning times and the last usage.
type watchState struct {
	mu       sync.Mutex
	usage    map[int64]Usage
	warned   map[string]time.Time // bot/limit → last warning
	reached  map[string]bool
	lastDisk map[int64]time.Time
}

// Usage returns the last measured usage of a bot.
func (r *Runner) Usage(id int64) (Usage, bool) {
	r.watch.mu.Lock()
	defer r.watch.mu.Unlock()
	u, ok := r.watch.usage[id]
	return u, ok
}

// Watch measures every running bot until ctx ends (plan of limits: RSS over
// the limit restarts the bot; 80 % of any limit warns once an hour; 100 %
// reports that the limit was reached).
func (r *Runner) Watch(ctx context.Context) {
	t := time.NewTicker(WatchEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.watchOnce()
		}
	}
}

func (r *Runner) procReader() ProcReader {
	if r.cfg.Proc != nil {
		return r.cfg.Proc
	}
	return procfs{}
}

func (r *Runner) watchOnce() {
	r.mu.Lock()
	ps := make([]*proc, 0, len(r.procs))
	for _, p := range r.procs {
		ps = append(ps, p)
	}
	r.mu.Unlock()
	pr := r.procReader()
	now := time.Now()
	for _, p := range ps {
		if p.pid == 0 {
			continue
		}
		id := p.bot.ID
		u, err := pr.Usage(p.pid)
		if err != nil {
			continue
		}
		r.watch.mu.Lock()
		prev := r.watch.usage[id]
		if now.Sub(r.watch.lastDisk[id]) >= WatchDiskEvery {
			r.watch.lastDisk[id] = now
			r.watch.mu.Unlock()
			u.FilesTotal, u.BiggestFile = pr.DirSize(r.BotDir(id))
			r.watch.mu.Lock()
		} else {
			u.FilesTotal, u.BiggestFile = prev.FilesTotal, prev.BiggestFile
		}
		r.watch.usage[id] = u
		r.watch.mu.Unlock()
		if r.cfg.Hooks.Usage != nil {
			r.cfg.Hooks.Usage(id, u)
		}
		r.checkLimits(p, u)
	}
}

func (r *Runner) checkLimits(p *proc, u Usage) {
	id := p.bot.ID
	l := p.bot.Settings.Limits.Or(r.cfg.DefaultLimits)
	if l.Memory > 0 && u.RSS >= l.Memory {
		r.coreLine(id, "memory %d MB of %d MB: restarting", u.RSS>>20, l.Memory>>20)
		r.emit(id, "bot_restarted", "error", map[string]any{"reason": "memory", "limit": "memory", "value": u.RSS, "max": l.Memory})
		go func() {
			r.mu.Lock()
			if r.procs[id] == p {
				p.stopping = true
			}
			r.mu.Unlock()
			r.terminate(p)
			if b, err := LoadBot(context.Background(), r.cfg.DB, id); err == nil {
				r.Start(context.Background(), b)
			}
		}()
		return
	}
	check := func(name string, v, max int64) {
		if max <= 0 {
			return
		}
		key := strconv.FormatInt(id, 10) + "/" + name
		r.watch.mu.Lock()
		defer r.watch.mu.Unlock()
		switch {
		case v >= max:
			if !r.watch.reached[key] {
				r.watch.reached[key] = true
				go r.emit(id, "limit_reached", "error", map[string]any{"limit": name, "value": v, "max": max})
			}
		case v*10 >= max*8:
			r.watch.reached[key] = false
			if time.Since(r.watch.warned[key]) >= warnEvery {
				r.watch.warned[key] = time.Now()
				go r.emit(id, "limit_warning", "warn", map[string]any{"limit": name, "value": v, "max": max})
			}
		default:
			r.watch.reached[key] = false
		}
	}
	check("memory", u.RSS, l.Memory)
	check("open_files", u.OpenFiles, l.OpenFiles)
	check("threads", u.Threads, l.Threads)
	check("files_total", u.FilesTotal, l.FilesTotal)
	check("file", u.BiggestFile, l.File)
}
