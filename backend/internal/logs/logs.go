// Package logs keeps bot output: files with rotation, the last lines in
// memory for a fast tail, live streams with filters, search and download.
package logs

import (
	"archive/zip"
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Levels detected in lines.
const (
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

var levelRank = map[string]int{LevelDebug: 0, LevelInfo: 1, LevelWarn: 2, LevelError: 3}

// Line is one stored line.
type Line struct {
	Seq    int64     `cbor:"1,keyasint" json:"seq"`
	Time   time.Time `cbor:"2,keyasint" json:"t"`
	Stream string    `cbor:"3,keyasint" json:"s"` // stdout | stderr | core | canary
	Level  string    `cbor:"4,keyasint" json:"l"`
	Text   string    `cbor:"5,keyasint" json:"m"`
}

// Rotation and memory limits (variables for tests).
var (
	FileSize  int64 = 10 << 20
	KeepFiles       = 10
	TailLines       = 5000
)

// DetectLevel takes "level" from a JSON line, or looks for words.
func DetectLevel(text string, stream string) string {
	t := strings.TrimSpace(text)
	if strings.HasPrefix(t, "{") {
		var m map[string]any
		if json.Unmarshal([]byte(t), &m) == nil {
			for _, k := range []string{"level", "lvl", "severity"} {
				if v, ok := m[k].(string); ok {
					return normLevel(v)
				}
			}
		}
	}
	lower := strings.ToLower(t)
	switch {
	case strings.HasPrefix(lower, "panic:") || strings.HasPrefix(lower, "fatal error:") ||
		containsWord(lower, "error") || containsWord(lower, "fatal") || containsWord(lower, "panic"):
		return LevelError
	case containsWord(lower, "warn") || containsWord(lower, "warning"):
		return LevelWarn
	case containsWord(lower, "debug"):
		return LevelDebug
	}
	return LevelInfo
}

func normLevel(v string) string {
	switch strings.ToLower(v) {
	case "debug", "trace":
		return LevelDebug
	case "warn", "warning":
		return LevelWarn
	case "error", "err", "fatal", "panic", "critical":
		return LevelError
	}
	return LevelInfo
}

func containsWord(s, w string) bool {
	for i := 0; ; {
		j := strings.Index(s[i:], w)
		if j < 0 {
			return false
		}
		j += i
		before := j == 0 || !isLetter(s[j-1])
		after := j+len(w) >= len(s) || !isLetter(s[j+len(w)])
		if before && after {
			return true
		}
		i = j + len(w)
	}
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

// Filter selects lines for streams and searches.
type Filter struct {
	MinLevel string `cbor:"1,keyasint" json:"level,omitempty"`
	Query    string `cbor:"2,keyasint" json:"q,omitempty"`
	Stream   string `cbor:"3,keyasint" json:"stream,omitempty"`
}

// Match reports whether a line passes the filter.
func (f Filter) Match(l Line) bool {
	if f.MinLevel != "" && levelRank[l.Level] < levelRank[f.MinLevel] {
		return false
	}
	if f.Stream != "" && l.Stream != f.Stream {
		return false
	}
	if f.Query != "" && !strings.Contains(strings.ToLower(l.Text), strings.ToLower(f.Query)) {
		return false
	}
	return true
}

// botLog is the log of one bot.
type botLog struct {
	dir  string
	mu   sync.Mutex
	f    *os.File
	w    *bufio.Writer
	size int64
	seq  int64
	tail []Line
	subs map[*Sub]struct{}
}

// Sub is a live subscription.
type Sub struct {
	C      chan Line
	filter Filter
	drops  int64
}

// Store holds logs of all bots.
type Store struct {
	root func(botID int64) string
	log  *slog.Logger

	mu   sync.Mutex
	bots map[int64]*botLog
	// OnLine is called for every line (panics parser, patterns, counters).
	OnLine func(botID int64, l Line)
}

// New returns a store; dir(botID) is the bot's log directory.
func New(dir func(botID int64) string, log *slog.Logger) *Store {
	if log == nil {
		log = slog.Default()
	}
	return &Store{root: dir, log: log, bots: map[int64]*botLog{}}
}

func (s *Store) bot(id int64) *botLog {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.bots[id]
	if b == nil {
		b = &botLog{dir: s.root(id), subs: map[*Sub]struct{}{}}
		s.bots[id] = b
	}
	return b
}

// Add stores a line of bot output.
func (s *Store) Add(botID int64, stream, text string, t time.Time) {
	l := Line{Time: t.UTC(), Stream: stream, Text: text, Level: DetectLevel(text, stream)}
	if stream == "core" {
		l.Level = LevelInfo
		if strings.Contains(text, "exited") || strings.Contains(text, "killing") || strings.Contains(text, "not ready") {
			l.Level = LevelWarn
		}
	}
	b := s.bot(botID)
	b.mu.Lock()
	b.seq++
	l.Seq = b.seq
	b.tail = append(b.tail, l)
	if len(b.tail) > TailLines {
		b.tail = append(b.tail[:0], b.tail[len(b.tail)-TailLines:]...)
	}
	if err := b.write(l); err != nil {
		s.log.Warn("writing bot log failed", "bot", botID, "err", err)
	}
	for sub := range b.subs {
		if !sub.filter.Match(l) {
			continue
		}
		select {
		case sub.C <- l:
		default:
			sub.drops++ // slow reader: drop rather than block the bot's output
		}
	}
	b.mu.Unlock()
	if s.OnLine != nil {
		s.OnLine(botID, l)
	}
}

func (b *botLog) write(l Line) error {
	if b.f == nil {
		if err := os.MkdirAll(b.dir, 0o750); err != nil {
			return err
		}
		f, err := os.OpenFile(filepath.Join(b.dir, "current.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
		if err != nil {
			return err
		}
		fi, _ := f.Stat()
		b.f, b.w, b.size = f, bufio.NewWriterSize(f, 32<<10), fi.Size()
	}
	data, _ := json.Marshal(l)
	data = append(data, '\n')
	n, err := b.w.Write(data)
	b.size += int64(n)
	if err != nil {
		return err
	}
	if err := b.w.Flush(); err != nil {
		return err
	}
	if b.size >= FileSize {
		return b.rotate()
	}
	return nil
}

// rotate renames current.log to <time>.log and drops the oldest files.
func (b *botLog) rotate() error {
	b.w.Flush()
	b.f.Close()
	b.f, b.w = nil, nil
	name := time.Now().UTC().Format("20060102T150405.000000000") + ".log"
	if err := os.Rename(filepath.Join(b.dir, "current.log"), filepath.Join(b.dir, name)); err != nil {
		return err
	}
	old := rotated(b.dir)
	for len(old) > KeepFiles-1 {
		os.Remove(filepath.Join(b.dir, old[0]))
		old = old[1:]
	}
	return nil
}

// rotated lists rotated files, oldest first.
func rotated(dir string) []string {
	ents, _ := os.ReadDir(dir)
	var out []string
	for _, e := range ents {
		if n := e.Name(); strings.HasSuffix(n, ".log") && n != "current.log" {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// Tail returns up to n last lines from memory that pass f.
func (s *Store) Tail(botID int64, n int, f Filter) []Line {
	b := s.bot(botID)
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []Line
	for i := len(b.tail) - 1; i >= 0 && len(out) < n; i-- {
		if f.Match(b.tail[i]) {
			out = append(out, b.tail[i])
		}
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// Subscribe starts a live stream of new lines.
func (s *Store) Subscribe(botID int64, f Filter) *Sub {
	sub := &Sub{C: make(chan Line, 1024), filter: f}
	b := s.bot(botID)
	b.mu.Lock()
	b.subs[sub] = struct{}{}
	b.mu.Unlock()
	return sub
}

// Unsubscribe ends a live stream.
func (s *Store) Unsubscribe(botID int64, sub *Sub) {
	b := s.bot(botID)
	b.mu.Lock()
	delete(b.subs, sub)
	b.mu.Unlock()
}

// Search scans the files (newest first) and returns up to limit matching
// lines, oldest first, within [from, to] when set.
func (s *Store) Search(botID int64, f Filter, from, to time.Time, limit int) ([]Line, error) {
	b := s.bot(botID)
	b.mu.Lock()
	if b.w != nil {
		b.w.Flush()
	}
	b.mu.Unlock()
	files := append(rotated(b.dir), "current.log")
	var out []Line
	for i := len(files) - 1; i >= 0 && len(out) < limit; i-- {
		lines, err := readFile(filepath.Join(b.dir, files[i]))
		if err != nil {
			continue
		}
		for j := len(lines) - 1; j >= 0 && len(out) < limit; j-- {
			l := lines[j]
			if (!from.IsZero() && l.Time.Before(from)) || (!to.IsZero() && l.Time.After(to)) || !f.Match(l) {
				continue
			}
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, nil
}

func readFile(path string) ([]Line, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Line
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var l Line
		if json.Unmarshal(sc.Bytes(), &l) == nil {
			out = append(out, l)
		}
	}
	return out, sc.Err()
}

// Zip writes all log files of a bot as a zip archive of plain text.
func (s *Store) Zip(botID int64, w io.Writer) error {
	b := s.bot(botID)
	b.mu.Lock()
	if b.w != nil {
		b.w.Flush()
	}
	b.mu.Unlock()
	zw := zip.NewWriter(w)
	for _, name := range append(rotated(b.dir), "current.log") {
		lines, err := readFile(filepath.Join(b.dir, name))
		if err != nil {
			continue
		}
		fw, err := zw.Create(strings.TrimSuffix(name, ".log") + ".txt")
		if err != nil {
			return err
		}
		bw := bufio.NewWriter(fw)
		for _, l := range lines {
			bw.WriteString(l.Time.Format("2006-01-02 15:04:05.000") + " [" + l.Stream + "] " + strings.ToUpper(l.Level[:1]) + " " + l.Text + "\n")
		}
		bw.Flush()
	}
	return zw.Close()
}

// Close flushes and closes all files.
func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, b := range s.bots {
		b.mu.Lock()
		if b.w != nil {
			b.w.Flush()
			b.f.Close()
			b.f, b.w = nil, nil
		}
		b.mu.Unlock()
	}
}

// Remove deletes a bot's logs (the bot was deleted).
func (s *Store) Remove(botID int64) error {
	b := s.bot(botID)
	b.mu.Lock()
	if b.f != nil {
		b.f.Close()
		b.f, b.w = nil, nil
	}
	b.mu.Unlock()
	s.mu.Lock()
	delete(s.bots, botID)
	s.mu.Unlock()
	return os.RemoveAll(b.dir)
}

// Dir returns the log directory of a bot.
func (s *Store) Dir(botID int64) string { return s.root(botID) }
