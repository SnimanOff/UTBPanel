package metrics

import "strings"

// Minute is everything counted for one bot in one minute. Names are open:
// new metrics (Bot API, DB, services, SDK) add keys, not fields. BotID 0 is
// the sum over all bots.
type Minute struct {
	ID       int64              `cbor:"1,keyasint" json:"id"`
	BotID    int64              `cbor:"2,keyasint" json:"bot_id"`
	TS       int64              `cbor:"3,keyasint" json:"ts"` // unix seconds, start of the minute
	Counters map[string]int64   `cbor:"4,keyasint,omitempty" json:"counters,omitempty"`
	Hists    map[string]Hist    `cbor:"5,keyasint,omitempty" json:"hists,omitempty"`
	Gauges   map[string]float64 `cbor:"6,keyasint,omitempty" json:"gauges,omitempty"` // max for system metrics, latest for SDK values
	// Uniq holds HyperLogLog sketches of users and chats seen this minute.
	Uniq map[string][]byte `cbor:"7,keyasint,omitempty" json:"-"`
}

// Names of common counters.
const (
	Updates        = "updates"
	Delivered      = "delivered"
	Errors         = "errors"
	Timeouts       = "timeouts"
	Unavailable    = "unavailable"
	Deferred       = "deferred"
	NewUsers       = "users_new"
	Blocked        = "users_blocked"
	Unblocked      = "users_unblocked"
	RespTime       = "resp"  // histogram
	UniqUsers      = "users" // HLL
	UniqChats      = "chats" // HLL
	PaymentsPrefix = "pay_"  // pay_<currency>_count, pay_<currency>_sum
	TypePrefix     = "type_" // type_message, type_callback_query…
	ChatPrefix     = "chat_" // chat_private, chat_group…
	DropPrefix     = "drop_" // drop_ban, drop_closed, drop_maintenance, drop_antiflood
	SourcePrefix   = "src_"  // new users by /start parameter
	LangPrefix     = "lang_" // updates by language
	CPU            = "cpu"   // gauge, % of one core
	RSS            = "rss"   // gauge, bytes
	OpenFiles      = "fds"   // gauge
	Threads        = "threads"
	CanaryPrefix   = "canary_" // canary_updates, canary_errors, canary_resp…
)

// Merge adds counters and histograms; system gauges take the maximum,
// while application gauges retain the latest value.
func (m *Minute) Merge(o *Minute) {
	if m.Counters == nil {
		m.Counters = map[string]int64{}
	}
	for k, v := range o.Counters {
		m.Counters[k] += v
	}
	if len(o.Hists) > 0 && m.Hists == nil {
		m.Hists = map[string]Hist{}
	}
	for k, h := range o.Hists {
		m.Hists[k] = m.Hists[k].Merge(h)
	}
	if len(o.Gauges) > 0 && m.Gauges == nil {
		m.Gauges = map[string]float64{}
	}
	for k, v := range o.Gauges {
		if previous, ok := m.Gauges[k]; !ok || v > previous || strings.HasPrefix(k, "sdk_") {
			m.Gauges[k] = v
		}
	}
}
