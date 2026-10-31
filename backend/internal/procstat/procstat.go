// Package procstat reads CPU time and memory of the current process.
package procstat

import (
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Stats is a snapshot of one process.
type Stats struct {
	Name       string  `cbor:"1,keyasint"`
	PID        int     `cbor:"2,keyasint"`
	CPUPercent float64 `cbor:"3,keyasint"` // of one core, since the previous sample
	RSS        int64   `cbor:"4,keyasint"` // bytes
	Goroutines int     `cbor:"5,keyasint"`
	Time       int64   `cbor:"6,keyasint"` // unix seconds
}

// Sampler computes CPU usage between calls.
type Sampler struct {
	mu       sync.Mutex
	lastCPU  time.Duration
	lastWall time.Time
}

// Sample returns stats of the current process.
func (s *Sampler) Sample(name string) Stats {
	cpu, rss := self()
	now := time.Now()
	s.mu.Lock()
	var pct float64
	if !s.lastWall.IsZero() {
		if wall := now.Sub(s.lastWall); wall > 0 {
			pct = float64(cpu-s.lastCPU) / float64(wall) * 100
		}
	}
	s.lastCPU, s.lastWall = cpu, now
	s.mu.Unlock()
	return Stats{
		Name: name, PID: os.Getpid(), CPUPercent: pct, RSS: rss,
		Goroutines: runtime.NumGoroutine(), Time: now.Unix(),
	}
}

// ClockTicks is USER_HZ on Linux; 100 on all supported platforms.
const ClockTicks = 100

// ParseStat returns user+system CPU time and RSS pages from /proc/<pid>/stat.
func ParseStat(data string) (cpu time.Duration, rssPages int64, ok bool) {
	// The command name may contain spaces; fields start after the last ')'.
	i := strings.LastIndexByte(data, ')')
	if i < 0 {
		return 0, 0, false
	}
	f := strings.Fields(data[i+1:])
	// f[0] is field 3 (state); utime=14, stime=15, rss=24.
	if len(f) < 22 {
		return 0, 0, false
	}
	ut, err1 := strconv.ParseInt(f[11], 10, 64)
	st, err2 := strconv.ParseInt(f[12], 10, 64)
	rss, err3 := strconv.ParseInt(f[21], 10, 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return 0, 0, false
	}
	return time.Duration(ut+st) * time.Second / ClockTicks, rss, true
}

func self() (time.Duration, int64) {
	if b, err := os.ReadFile("/proc/self/stat"); err == nil {
		if cpu, pages, ok := ParseStat(string(b)); ok {
			return cpu, pages * int64(os.Getpagesize())
		}
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return 0, int64(m.Sys)
}
