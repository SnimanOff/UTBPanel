package logs

import "testing"

func TestMatcher(t *testing.T) {
	counts := map[string]int{}
	var limits []string
	m := &Matcher{
		Count: func(_ int64, name string) { counts[name]++ },
		Limit: func(_ int64, limit, _ string) { limits = append(limits, limit) },
	}
	ps := []Pattern{{Name: "orders", Pattern: "order created"}, {Name: "slow_sql", Pattern: `sql took \d{4,} ms`, Regexp: true}}
	if err := Validate(ps); err != nil {
		t.Fatal(err)
	}
	if Validate([]Pattern{{Name: "Bad Name", Pattern: "x"}}) == nil || Validate([]Pattern{{Name: "x", Pattern: "(", Regexp: true}}) == nil {
		t.Fatal("bad patterns accepted")
	}
	m.Set(1, ps)
	m.Line(1, "INFO order created id=5")
	m.Line(1, "INFO order created id=6")
	m.Line(1, "WARN sql took 1500 ms")
	m.Line(1, "WARN sql took 150 ms")
	m.Line(2, "INFO order created") // another bot has no patterns
	if counts["log_orders"] != 2 || counts["log_slow_sql"] != 1 {
		t.Fatalf("%v", counts)
	}
	m.Line(1, "accept tcp: accept4: too many open files")
	m.Line(1, "accept tcp: accept4: Too many open files") // within 10 minutes
	if len(limits) != 1 || limits[0] != "open_files" {
		t.Fatalf("%v", limits)
	}
}
