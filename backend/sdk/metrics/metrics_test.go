package metrics

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

type fakeClient struct {
	online  bool
	fail    bool
	batches []Batch
}

func (f *fakeClient) Connected() bool  { return f.online }
func (f *fakeClient) Use(string) error { return nil }
func (f *fakeClient) Call(_ context.Context, _ string, in, out any) error {
	f.batches = append(f.batches, in.(Batch))
	if f.fail {
		return errors.New("response lost")
	}
	return nil
}

func TestBatchRetryKeepsSequenceAndDelta(t *testing.T) {
	f := &fakeClient{online: true, fail: true}
	r := New(f)
	counter := r.Counter("orders")
	counter.Add(3)
	if err := r.Flush(context.Background()); err == nil {
		t.Fatal("failure lost")
	}
	counter.Add(2)
	f.fail = false
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(f.batches) != 2 || f.batches[0].Sequence != f.batches[1].Sequence || f.batches[1].Points[0].Delta != 3 {
		t.Fatalf("%+v", f.batches)
	}
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.batches[2].Sequence != 2 || f.batches[2].Points[0].Delta != 2 {
		t.Fatalf("%+v", f.batches)
	}
}

func TestAutonomousMetricsAndLimit(t *testing.T) {
	f := &fakeClient{}
	r := New(f)
	c := r.Counter("orders")
	c.Add(10)
	v := r.Value("depth")
	v.Set(-3)
	if err := r.Flush(context.Background()); err != nil || len(f.batches) != 0 {
		t.Fatal("autonomous metrics sent")
	}
	f.online = true
	c.Add(2)
	v.Set(0)
	for i := 0; i < MaxNames; i++ {
		r.Counter(fmt.Sprintf("extra_%d", i)).Add(1)
	}
	if err := r.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	b := f.batches[0]
	if len(b.Points) != MaxNames || b.Dropped != 2 {
		t.Fatalf("%d %d", len(b.Points), b.Dropped)
	}
	for _, p := range b.Points {
		if p.Name == "orders" && p.Delta != 2 {
			t.Fatal(p)
		}
		if p.Name == "depth" && p.Value != 0 {
			t.Fatal(p)
		}
	}
}
