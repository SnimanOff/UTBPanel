package logs

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Pattern is a bot's own log pattern: every matching line adds one to the
// counter log_<name> of that minute.
type Pattern struct {
	Name    string `json:"name"`
	Pattern string `json:"pattern"`
	Regexp  bool   `json:"regexp,omitempty"`
}

var nameRe = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

// Validate checks a list of patterns (at most 20).
func Validate(ps []Pattern) error {
	if len(ps) > 20 {
		return fmt.Errorf("не больше 20 шаблонов")
	}
	seen := map[string]bool{}
	for _, p := range ps {
		if !nameRe.MatchString(p.Name) {
			return fmt.Errorf("имя %q: латиница в нижнем регистре, цифры и _", p.Name)
		}
		if seen[p.Name] {
			return fmt.Errorf("имя %q повторяется", p.Name)
		}
		seen[p.Name] = true
		if p.Pattern == "" {
			return fmt.Errorf("пустой шаблон %q", p.Name)
		}
		if p.Regexp {
			if _, err := regexp.Compile(p.Pattern); err != nil {
				return fmt.Errorf("шаблон %q: %v", p.Name, err)
			}
		}
	}
	return nil
}

type compiled struct {
	name string
	sub  string
	re   *regexp.Regexp
}

// Builtin limit messages: the bot hit a resource limit (raised as
// limit_reached even when the process survives).
var Builtin = []struct{ Limit, Text string }{
	{"open_files", "too many open files"},
	{"file_size", "file too large"},
	{"threads", "pthread_create failed"},
}

// Matcher counts matches of patterns per bot.
type Matcher struct {
	// Count adds to a counter of the bot's current minute.
	Count func(botID int64, name string)
	// Limit reports a built-in limit message (at most every 10 minutes).
	Limit func(botID int64, limit, line string)

	mu   sync.RWMutex
	bots map[int64][]compiled
	last map[string]time.Time
}

// Set replaces the patterns of a bot.
func (m *Matcher) Set(botID int64, ps []Pattern) {
	var cs []compiled
	for _, p := range ps {
		c := compiled{name: p.Name}
		if p.Regexp {
			re, err := regexp.Compile(p.Pattern)
			if err != nil {
				continue
			}
			c.re = re
		} else {
			c.sub = p.Pattern
		}
		cs = append(cs, c)
	}
	m.mu.Lock()
	if m.bots == nil {
		m.bots = map[int64][]compiled{}
	}
	m.bots[botID] = cs
	m.mu.Unlock()
}

// Line checks one output line of a bot.
func (m *Matcher) Line(botID int64, text string) {
	m.mu.RLock()
	cs := m.bots[botID]
	m.mu.RUnlock()
	for _, c := range cs {
		if (c.re != nil && c.re.MatchString(text)) || (c.re == nil && strings.Contains(text, c.sub)) {
			if m.Count != nil {
				m.Count(botID, "log_"+c.name)
			}
		}
	}
	if m.Limit == nil {
		return
	}
	low := strings.ToLower(text)
	for _, b := range Builtin {
		if !strings.Contains(low, b.Text) {
			continue
		}
		key := fmt.Sprint(botID, b.Limit)
		m.mu.Lock()
		if m.last == nil {
			m.last = map[string]time.Time{}
		}
		fire := time.Since(m.last[key]) > 10*time.Minute
		if fire {
			m.last[key] = time.Now()
		}
		m.mu.Unlock()
		if fire {
			m.Limit(botID, b.Limit, text)
		}
	}
}
