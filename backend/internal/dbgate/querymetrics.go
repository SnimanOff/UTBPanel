package dbgate

import (
	"strings"
	"sync"
	"time"
)

type QueryBucket struct {
	Milliseconds float64 `cbor:"1,keyasint" json:"le_ms"`
	Count        int64   `cbor:"2,keyasint" json:"count"`
}
type SlowQuery struct {
	Time         time.Time `cbor:"1,keyasint" json:"time"`
	SQL          string    `cbor:"2,keyasint" json:"sql"`
	Milliseconds float64   `cbor:"3,keyasint" json:"duration_ms"`
	Failed       bool      `cbor:"4,keyasint" json:"failed"`
}
type QueryStats struct {
	Counts    map[string]int64 `cbor:"1,keyasint" json:"counts"`
	Active    int              `cbor:"2,keyasint" json:"active"`
	Failures  int64            `cbor:"3,keyasint" json:"failures"`
	Histogram []QueryBucket    `cbor:"4,keyasint" json:"histogram"`
	Slow      []SlowQuery      `cbor:"5,keyasint" json:"slow"`
}

func emptyStats() *QueryStats {
	s := &QueryStats{Counts: map[string]int64{"SELECT": 0, "INSERT": 0, "UPDATE": 0, "DELETE": 0, "other": 0}, Slow: []SlowQuery{}}
	for _, ms := range []float64{1, 5, 10, 25, 50, 100, 250, 500, 1000, 5000, 0} {
		s.Histogram = append(s.Histogram, QueryBucket{Milliseconds: ms})
	}
	return s
}

func (g *Gateway) statsFor(id int64) *QueryStats {
	if g.stats == nil {
		g.stats = map[int64]*QueryStats{}
	}
	if g.stats[id] == nil {
		g.stats[id] = emptyStats()
	}
	return g.stats[id]
}

func (g *Gateway) Snapshot(id int64) QueryStats {
	g.statsMu.Lock()
	defer g.statsMu.Unlock()
	s := g.statsFor(id)
	out := *s
	out.Counts = map[string]int64{}
	for k, v := range s.Counts {
		out.Counts[k] = v
	}
	out.Histogram = append([]QueryBucket{}, s.Histogram...)
	out.Slow = append([]SlowQuery{}, s.Slow...)
	return out
}

func (g *Gateway) startQuery(id int64, text string) func(error) {
	kind, safeSQL := summarizeSQL(text)
	started := time.Now()
	g.statsMu.Lock()
	g.statsFor(id).Active++
	g.statsMu.Unlock()
	var once sync.Once
	return func(err error) {
		once.Do(func() {
			d := time.Since(started)
			threshold := g.SlowThreshold
			if threshold <= 0 {
				threshold = 50 * time.Millisecond
			}
			ms := float64(d) / float64(time.Millisecond)
			g.statsMu.Lock()
			s := g.statsFor(id)
			s.Active--
			s.Counts[kind]++
			if err != nil {
				s.Failures++
			}
			for i := range s.Histogram {
				if s.Histogram[i].Milliseconds == 0 || ms <= s.Histogram[i].Milliseconds {
					s.Histogram[i].Count++
					break
				}
			}
			if d > threshold {
				s.Slow = append(s.Slow, SlowQuery{Time: time.Now().UTC(), SQL: safeSQL, Milliseconds: ms, Failed: err != nil})
				if len(s.Slow) > 20 {
					s.Slow = s.Slow[len(s.Slow)-20:]
				}
			}
			g.statsMu.Unlock()
			if g.Observe != nil {
				g.Observe(id, kind, d, err)
			}
		})
	}
}

// Remove all quoted data, numeric literals and comments before storing SQL.
// This is a lexical summary, not a security check: SQLite's authorizer and
// filesystem credentials enforce access independently of the query text.
func summarizeSQL(sql string) (string, string) {
	var out strings.Builder
	first, kind := true, "other"
	with, depth := false, 0
	for i := 0; i < len(sql); {
		c := sql[i]
		if c == '-' && i+1 < len(sql) && sql[i+1] == '-' {
			for i < len(sql) && sql[i] != '\n' {
				i++
			}
			out.WriteByte(' ')
			continue
		}
		if c == '/' && i+1 < len(sql) && sql[i+1] == '*' {
			i += 2
			for i+1 < len(sql) && !(sql[i] == '*' && sql[i+1] == '/') {
				i++
			}
			i = min(len(sql), i+2)
			out.WriteByte(' ')
			continue
		}
		if c == '\'' || c == '"' || c == '`' || c == '[' {
			end := c
			if c == '[' {
				end = ']'
			}
			i++
			for i < len(sql) {
				if sql[i] == end {
					i++
					if i < len(sql) && sql[i] == end {
						i++
						continue
					}
					break
				}
				i++
			}
			out.WriteByte('?')
			continue
		}
		if c >= '0' && c <= '9' || c == '.' && i+1 < len(sql) && sql[i+1] >= '0' && sql[i+1] <= '9' {
			i++
			for i < len(sql) {
				n := sql[i]
				if !(n >= '0' && n <= '9' || n >= 'a' && n <= 'f' || n >= 'A' && n <= 'F' || n == 'x' || n == 'X' || n == '.' || n == '+' || n == '-') {
					break
				}
				i++
			}
			out.WriteByte('?')
			continue
		}
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '_' {
			start := i
			i++
			for i < len(sql) {
				n := sql[i]
				if !(n >= 'a' && n <= 'z' || n >= 'A' && n <= 'Z' || n >= '0' && n <= '9' || n == '_') {
					break
				}
				i++
			}
			word := sql[start:i]
			upper := strings.ToUpper(word)
			isFirst := first
			if first {
				first = false
				with = upper == "WITH"
			}
			if depth == 0 && (isFirst || with && kind == "other") {
				switch upper {
				case "SELECT", "INSERT", "UPDATE", "DELETE":
					kind = upper
				}
			}
			out.WriteString(word)
			continue
		}
		if c == '(' {
			depth++
		}
		if c == ')' && depth > 0 {
			depth--
		}
		out.WriteByte(c)
		i++
	}
	safe := strings.Join(strings.Fields(out.String()), " ")
	if len(safe) > 4096 {
		safe = safe[:4096]
	}
	return kind, safe
}
