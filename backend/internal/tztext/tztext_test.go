package tztext

import (
	"testing"
	"time"
)

func TestPairAndRender(t *testing.T) {
	msk := Load("")
	at := time.Date(2026, 11, 1, 18, 0, 0, 0, time.UTC)
	if got := Pair(at, msk); got != "18:00 UTC = 21:00 МСК" {
		t.Fatalf("%q", got)
	}
	late := time.Date(2026, 11, 1, 22, 30, 0, 0, time.UTC)
	if got := Pair(late, msk); got != "22:30 UTC = 02.11 01:30 МСК" {
		t.Fatalf("%q", got)
	}
	if got := Pair(at, Load("UTC")); got != "18:00 UTC" {
		t.Fatalf("%q", got)
	}
	if Load("Nowhere/City").String() != Default {
		t.Fatal("unknown zone must fall back")
	}
	vl := Load("Asia/Vladivostok")
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC) // 20:00 there
	got := Render("с {start} до {end} "+In(vl, now), now.Add(time.Hour), now.Add(5*time.Hour), vl, now)
	if got != "с 21:00 до 02.11 01:00 по ВЛАД" {
		t.Fatalf("%q", got)
	}
}
