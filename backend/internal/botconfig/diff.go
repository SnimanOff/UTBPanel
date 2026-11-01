package botconfig

import (
	"bytes"
	"encoding/json"
	"reflect"
	"sort"
	"strings"
)

type Change struct {
	Path   string          `json:"path"`
	Before json.RawMessage `json:"before"`
	After  json.RawMessage `json:"after"`
	Level  string          `json:"level"`
}
type Difference struct {
	Changes []Change `json:"changes"`
	Confirm bool     `json:"confirm"`
	Restart bool     `json:"restart"`
}

func Diff(before, after Document) Difference {
	before.Version = ""
	after.Version = ""
	before.Group = nil
	after.Group = nil
	a, _ := json.Marshal(before)
	b, _ := json.Marshal(after)
	var left, right map[string]any
	dec := json.NewDecoder(bytes.NewReader(a))
	dec.UseNumber()
	dec.Decode(&left)
	dec = json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	dec.Decode(&right)
	d := Difference{Changes: []Change{}}
	var visit func(string, any, any)
	visit = func(path string, a, b any) {
		if reflect.DeepEqual(a, b) {
			return
		}
		am, aok := a.(map[string]any)
		bm, bok := b.(map[string]any)
		if aok && bok {
			keys := map[string]bool{}
			for k := range am {
				keys[k] = true
			}
			for k := range bm {
				keys[k] = true
			}
			ordered := []string{}
			for k := range keys {
				ordered = append(ordered, k)
			}
			sort.Strings(ordered)
			for _, k := range ordered {
				next := k
				if path != "" {
					next = path + "." + k
				}
				visit(next, am[k], bm[k])
			}
			return
		}
		level := "write"
		for _, prefix := range []string{"env", "rename", "limits", "github", "stop", "crash_updates", "antiflood.enabled", "closed.enabled"} {
			if path == prefix || strings.HasPrefix(path, prefix+".") {
				level = "confirm"
			}
		}
		if path == "secrets" {
			old, _ := a.([]any)
			next, _ := b.([]any)
			present := map[any]bool{}
			for _, name := range next {
				present[name] = true
			}
			for _, name := range old {
				if !present[name] {
					level = "confirm"
				}
			}
			d.Restart = true
		}
		if strings.HasPrefix(path, "env.") || strings.HasPrefix(path, "rename.") {
			d.Restart = true
		}
		old, _ := json.Marshal(a)
		next, _ := json.Marshal(b)
		d.Changes = append(d.Changes, Change{Path: path, Before: old, After: next, Level: level})
		if level == "confirm" {
			d.Confirm = true
		}
	}
	visit("", left, right)
	return d
}
