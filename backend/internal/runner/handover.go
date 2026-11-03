package runner

import (
	"errors"
	"io"
	"os"
	"time"
)

// Handed describes a bot process given to the next core process during a
// seamless restart. Fd fields index the files passed with it.
type Handed struct {
	Bot     Bot       `json:"bot"`
	PID     int       `json:"pid"`
	Started time.Time `json:"started"`
	Stdout  int       `json:"stdout"`
	Stderr  int       `json:"stderr"`
	PidFD   int       `json:"pidfd"`
}

// ErrExitUnknown is the exit of a handed-over process: it is not a child of
// this core, so only the fact of its exit is known.
var ErrExitUnknown = errors.New("runner: exited (code unknown after core restart)")

// ErrNoHandover means this system cannot pass processes on.
var ErrNoHandover = errors.New("runner: seamless restart needs Linux")

func pipeFile(rc io.ReadCloser) *os.File {
	f, _ := rc.(*os.File)
	return f
}

// Handover lists the running processes and the files the next core needs
// to watch them: stdout, stderr and a pidfd of each. Nothing changes here
// until Release; on failure the caller closes the files.
func (r *Runner) Handover() ([]Handed, []*os.File, error) {
	r.mu.Lock()
	ps := make([]*proc, 0, len(r.procs)+len(r.canaries))
	for _, p := range r.procs {
		ps = append(ps, p)
	}
	for _, p := range r.canaries {
		ps = append(ps, p)
	}
	r.mu.Unlock()
	var out []Handed
	var files []*os.File
	for _, p := range ps {
		if p.out == nil || p.errOut == nil || p.pid == 0 {
			continue
		}
		fd, err := pidfd(p.pid)
		if err != nil {
			for _, f := range files {
				f.Close()
			}
			return nil, nil, err
		}
		h := Handed{Bot: p.bot, PID: p.pid, Started: p.started, Stdout: len(files), Stderr: len(files) + 1, PidFD: len(files) + 2}
		files = append(files, p.out, p.errOut, fd)
		out = append(out, h)
	}
	return out, files, nil
}

// Release stops watching every process: they belong to the next core now.
// Their exits are no longer crashes here, and the bots are not stopped on
// shutdown.
func (r *Runner) Release() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.procs {
		p.detached = true
	}
	for _, p := range r.canaries {
		p.detached = true
	}
	r.procs = map[int64]*proc{}
	r.canaries = map[int64]*proc{}
	for id, s := range r.sups {
		s.mu.Lock()
		if s.timer != nil {
			s.timer.Stop()
		}
		s.mu.Unlock()
		delete(r.sups, id)
	}
}

// Adopt takes the processes handed over by the previous core: their output
// is read and their exit watched as if this core had started them.
func (r *Runner) Adopt(hs []Handed, files []*os.File) {
	for _, h := range hs {
		p := &proc{bot: h.Bot, pid: h.PID, out: files[h.Stdout], errOut: files[h.Stderr], started: h.Started, done: make(chan struct{})}
		instance := h.Bot.Instance
		r.mu.Lock()
		if instance == "canary" {
			if r.canaries == nil {
				r.canaries = map[int64]*proc{}
			}
			r.canaries[h.Bot.ID] = p
		} else {
			r.procs[h.Bot.ID] = p
		}
		r.mu.Unlock()
		go r.pipe(h.Bot.ID, instance, "stdout", p.out)
		go r.pipe(h.Bot.ID, instance, "stderr", p.errOut)
		pfd := files[h.PidFD]
		go func() {
			waitPidFD(pfd)
			pfd.Close()
			p.exit = ErrExitUnknown
			if instance != "canary" {
				r.forget(p)
				close(p.done)
			} else {
				r.mu.Lock()
				if r.canaries[h.Bot.ID] == p {
					delete(r.canaries, h.Bot.ID)
				}
				stopping := p.stopping || p.detached
				r.mu.Unlock()
				close(p.done)
				if !stopping {
					r.coreLine(h.Bot.ID, "canary %s exited", h.Bot.Version)
					r.emit(h.Bot.ID, "canary_crashed", "error", map[string]any{"version": h.Bot.Version, "exit": -1, "error": ErrExitUnknown.Error()})
				}
				return
			}
			r.exited(p)
		}()
		r.coreLine(h.Bot.ID, "taken over from the previous core (pid %d)", h.PID)
	}
}
