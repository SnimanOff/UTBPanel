package deploy

import (
	"testing"
	"time"
)

func TestJudge(t *testing.T) {
	r := DefaultRules
	cases := []struct {
		s    Stats
		want string
	}{
		{Stats{Elapsed: 5 * time.Second, Running: true}, Watching},
		{Stats{Elapsed: 61 * time.Second, Running: true, OK: 100, Bad: 5}, Passed},
		{Stats{Elapsed: 20 * time.Second, Running: true, Crashes: 2}, Rollback},
		{Stats{Elapsed: 10 * time.Second, Running: false}, Watching}, // still starting
		{Stats{Elapsed: 16 * time.Second, Running: false}, Rollback},
		{Stats{Elapsed: 30 * time.Second, Running: true, OK: 7, Bad: 3}, Rollback},   // 30 %
		{Stats{Elapsed: 30 * time.Second, Running: true, OK: 1, Bad: 3}, Watching},   // too few updates
		{Stats{Elapsed: 30 * time.Second, Running: true, OK: 80, Bad: 20}, Watching}, // exactly 20 %
	}
	for i, c := range cases {
		if got, why := r.Judge(c.s); got != c.want {
			t.Errorf("%d: %+v → %q (%s), want %q", i, c.s, got, why, c.want)
		}
	}
}
