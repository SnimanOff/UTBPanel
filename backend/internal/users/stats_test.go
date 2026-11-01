package users

import (
	"context"
	"testing"
)

func TestStatsEmpty(t *testing.T) {
	tr := newTracker(t)
	s, err := tr.Stats(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if s.Total != 0 {
		t.Fatalf("%+v", s)
	}
}
