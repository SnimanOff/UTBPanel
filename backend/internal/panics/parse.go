// Package panics finds Go panics and fatal errors in a bot's output and
// groups them: the same kind of panic in the same place of the bot's code
// is one group, however many times it happens.
package panics

import (
	"regexp"
	"strings"
	"sync"
	"time"
)

// Panic is one block with its stack trace.
type Panic struct {
	BotID     int64     `cbor:"1,keyasint" json:"bot_id"`
	Time      time.Time `cbor:"2,keyasint" json:"ts"`
	Kind      string    `cbor:"3,keyasint" json:"kind"`  // "runtime error: index out of range"
	Frame     string    `cbor:"4,keyasint" json:"frame"` // "main.(*Bot).handle (bot.go:42)"
	Signature string    `cbor:"5,keyasint" json:"signature"`
	Stack     string    `cbor:"6,keyasint" json:"stack"`
	Version   string    `cbor:"7,keyasint" json:"version,omitempty"`
}

// MaxLines bounds one block; the rest of a huge dump is cut.
const MaxLines = 400

// Parser collects blocks of one bot from its output lines.
type Parser struct {
	// Module is the bot's module path (buildinfo); its frames come first.
	Module func() string
	// Found receives every complete block.
	Found func(Panic)

	mu    sync.Mutex
	lines []string
	at    time.Time
	stack bool // the goroutine part has begun
	timer *time.Timer
	bot   int64
}

var (
	goroutineRe = regexp.MustCompile(`^goroutine \d+ \[`)
	funcRe      = regexp.MustCompile(`^[\w./*()\-%\[\]]+\(.*\)$`)
)

func isStart(l string) bool {
	return strings.HasPrefix(l, "panic: ") || strings.HasPrefix(l, "fatal error: ")
}

// stackLine reports lines that belong to a trace.
func stackLine(l string) bool {
	switch {
	case l == "", strings.HasPrefix(l, "\t"), goroutineRe.MatchString(l), strings.HasPrefix(l, "created by "),
		strings.HasPrefix(l, "panic: "), strings.HasPrefix(l, "[recovered]"), strings.HasPrefix(l, "\tpanic: "),
		strings.HasPrefix(l, "...additional frames elided..."), strings.HasPrefix(l, "exit status "):
		return true
	}
	return funcRe.MatchString(l)
}

// Line feeds one output line of the bot.
func (p *Parser) Line(botID int64, text string, at time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.lines) == 0 {
		if !isStart(text) {
			return
		}
		p.lines, p.at, p.stack, p.bot = []string{text}, at, false, botID
		p.arm()
		return
	}
	if goroutineRe.MatchString(text) {
		p.stack = true
	}
	// The first line that is not part of the trace ends the block (only
	// after the goroutines began: the message may span lines).
	if p.stack && !stackLine(text) {
		p.flushLocked()
		if isStart(text) {
			p.lines, p.at, p.stack, p.bot = []string{text}, at, false, botID
			p.arm()
		}
		return
	}
	if len(p.lines) < MaxLines {
		p.lines = append(p.lines, text)
	}
	p.arm()
}

// arm ends the block after 2 s of silence (the process exited).
func (p *Parser) arm() {
	if p.timer != nil {
		p.timer.Stop()
	}
	p.timer = time.AfterFunc(2*time.Second, p.Flush)
}

// Flush ends the current block (call it when the process exits).
func (p *Parser) Flush() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.flushLocked()
}

func (p *Parser) flushLocked() {
	if len(p.lines) == 0 {
		return
	}
	lines := p.lines
	p.lines = nil
	if p.timer != nil {
		p.timer.Stop()
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	module := ""
	if p.Module != nil {
		module = p.Module()
	}
	pn := Analyze(lines, module)
	pn.BotID, pn.Time = p.bot, p.at.UTC()
	if p.Found != nil {
		go p.Found(pn)
	}
}

var (
	numRe   = regexp.MustCompile(`0x[0-9a-fA-F]+|\d+`)
	brackRe = regexp.MustCompile(`\[[^\]]*\]`)
	addrRe  = regexp.MustCompile(` \+0x[0-9a-f]+$`)
)

// Kind is the panic message without numbers and addresses, so repeats with
// other values fall into one group.
func Kind(first string) string {
	msg := strings.TrimPrefix(strings.TrimPrefix(first, "panic: "), "fatal error: ")
	if strings.HasPrefix(first, "fatal error: ") {
		msg = "fatal error: " + msg
	}
	msg = strings.TrimSuffix(msg, " [recovered]")
	msg = brackRe.ReplaceAllString(msg, "")
	msg = numRe.ReplaceAllString(msg, "N")
	msg = strings.Join(strings.Fields(msg), " ")
	if r := []rune(msg); len(r) > 120 {
		msg = string(r[:120]) + "…"
	}
	return msg
}

// std reports frames of the runtime and the standard library: the first
// path element of the function has no dot ("runtime.", "net/http.").
func std(fn string) bool {
	first, _, _ := strings.Cut(fn, "/")
	if !strings.Contains(fn, "/") {
		first, _, _ = strings.Cut(fn, ".")
		return first != "main"
	}
	return !strings.Contains(first, ".")
}

// Analyze finds the kind and the first frame of the bot's own code.
func Analyze(lines []string, module string) Panic {
	pn := Panic{Stack: strings.Join(lines, "\n")}
	if len(lines) > 0 {
		pn.Kind = Kind(lines[0])
	}
	var fallback string
	inPanicking := false
	for i := 0; i+1 < len(lines); i++ {
		l := lines[i]
		if goroutineRe.MatchString(l) {
			inPanicking = true
			continue
		}
		if !inPanicking || !funcRe.MatchString(l) || !strings.HasPrefix(lines[i+1], "\t") {
			continue
		}
		fn := l[:strings.LastIndex(l, "(")]
		loc := strings.TrimSpace(addrRe.ReplaceAllString(lines[i+1], ""))
		file := loc
		if j := strings.LastIndex(loc, "/"); j >= 0 {
			file = loc[j+1:]
		}
		frame := fn + " (" + file + ")"
		mine := module != "" && (strings.HasPrefix(fn, module+".") || strings.HasPrefix(fn, module+"/"))
		if mine || (module == "" && !std(fn)) || strings.HasPrefix(fn, "main.") {
			pn.Frame = frame
			break
		}
		if fallback == "" && !std(fn) {
			fallback = frame
		}
		// Only the first goroutine (the one that panicked) counts.
		if i+2 < len(lines) && lines[i+2] == "" {
			break
		}
	}
	if pn.Frame == "" {
		pn.Frame = fallback
	}
	// The line number is not part of the group: edits move it.
	fnOnly := pn.Frame
	if j := strings.Index(fnOnly, " ("); j >= 0 {
		fnOnly = fnOnly[:j]
	}
	pn.Signature = pn.Kind + " @ " + fnOnly
	return pn
}
