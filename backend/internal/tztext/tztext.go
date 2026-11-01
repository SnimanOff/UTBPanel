// Package tztext formats times for texts that bot users read: in the bot's
// zone, "21:00" today and "02.11 21:00" otherwise. The zone database is
// built in, so the edge and the core render the same on any image.
package tztext

import (
	"strings"
	"time"
	_ "time/tzdata"
)

// Default is the zone of texts unless the bot sets another.
const Default = "Europe/Moscow"

// Load returns the zone by its IANA name, the default one for an empty or
// unknown name.
func Load(name string) *time.Location {
	if name == "" {
		name = Default
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		loc, _ = time.LoadLocation(Default)
	}
	return loc
}

// Clock is "21:00" when t is today in loc and "02.11 21:00" otherwise.
func Clock(t time.Time, loc *time.Location, now time.Time) string {
	if t.IsZero() {
		return "—"
	}
	lt, ln := t.In(loc), now.In(loc)
	if lt.YearDay() == ln.YearDay() && lt.Year() == ln.Year() {
		return lt.Format("15:04")
	}
	return lt.Format("02.01 15:04")
}

// Render puts times into {start} and {end}.
func Render(text string, start, end time.Time, loc *time.Location, now time.Time) string {
	return strings.NewReplacer("{start}", Clock(start, loc, now), "{end}", Clock(end, loc, now)).Replace(text)
}

// abbrs are names users know; other zones show their own abbreviation or
// offset.
var abbrs = map[string]string{
	"Europe/Moscow":      "МСК",
	"Europe/Kaliningrad": "КЛД",
	"Europe/Samara":      "САМТ",
	"Asia/Yekaterinburg": "ЕКБ",
	"Asia/Novosibirsk":   "НСК",
	"Asia/Vladivostok":   "ВЛАД",
	"UTC":                "UTC",
}

// Abbr is a short name of the zone: "МСК", "CET", "+05".
func Abbr(loc *time.Location, at time.Time) string {
	if a, ok := abbrs[loc.String()]; ok {
		return a
	}
	return at.In(loc).Format("MST")
}

// Pair is shown in forms next to a UTC time: "18:00 UTC = 21:00 МСК".
func Pair(t time.Time, loc *time.Location) string {
	u := t.UTC().Format("15:04") + " UTC"
	if loc.String() == "UTC" {
		return u
	}
	l := t.In(loc)
	s := l.Format("15:04")
	if l.YearDay() != t.UTC().YearDay() {
		s = l.Format("02.01 15:04")
	}
	return u + " = " + s + " " + Abbr(loc, t)
}

// In is "по Москве" style wording for texts: "по МСК".
func In(loc *time.Location, at time.Time) string {
	if loc.String() == Default {
		return "по Москве"
	}
	return "по " + Abbr(loc, at)
}
