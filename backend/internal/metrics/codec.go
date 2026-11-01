package metrics

import (
	"encoding/binary"
	"errors"
	"math"
	"sort"
	"strings"
)

// The stored form of a minute (hour, day): a version byte, then counters,
// histograms and gauges, each with only non-zero values, as varints.
// About 200–400 bytes per bot per minute.
const codecVersion = 1

// Encode packs counters, histograms and gauges of m.
func Encode(m *Minute) []byte {
	b := []byte{codecVersion}
	keys := func(n int, each func(func(string))) []string {
		out := make([]string, 0, n)
		each(func(k string) { out = append(out, k) })
		sort.Strings(out)
		return out
	}
	str := func(s string) {
		b = binary.AppendUvarint(b, uint64(len(s)))
		b = append(b, s...)
	}
	var ck []string
	for _, k := range keys(len(m.Counters), func(f func(string)) {
		for k := range m.Counters {
			f(k)
		}
	}) {
		if m.Counters[k] != 0 {
			ck = append(ck, k)
		}
	}
	b = binary.AppendUvarint(b, uint64(len(ck)))
	for _, k := range ck {
		str(k)
		b = binary.AppendVarint(b, m.Counters[k])
	}
	hk := keys(len(m.Hists), func(f func(string)) {
		for k, h := range m.Hists {
			if h.Count() > 0 {
				f(k)
			}
		}
	})
	b = binary.AppendUvarint(b, uint64(len(hk)))
	for _, k := range hk {
		str(k)
		h := m.Hists[k]
		n := 0
		for _, v := range h {
			if v != 0 {
				n++
			}
		}
		b = binary.AppendUvarint(b, uint64(n))
		for i, v := range h {
			if v != 0 {
				b = binary.AppendUvarint(b, uint64(i))
				b = binary.AppendUvarint(b, v)
			}
		}
	}
	gk := keys(len(m.Gauges), func(f func(string)) {
		for k, v := range m.Gauges {
			if v != 0 || strings.HasPrefix(k, "sdk_") {
				f(k)
			}
		}
	})
	b = binary.AppendUvarint(b, uint64(len(gk)))
	for _, k := range gk {
		str(k)
		b = binary.AppendUvarint(b, math.Float64bits(m.Gauges[k]))
	}
	return b
}

var errCodec = errors.New("metrics: damaged record")

// Decode unpacks data into m (counters, histograms, gauges).
func Decode(data []byte, m *Minute) error {
	if len(data) == 0 || data[0] != codecVersion {
		return errCodec
	}
	p := data[1:]
	uv := func() (uint64, bool) {
		v, n := binary.Uvarint(p)
		if n <= 0 {
			return 0, false
		}
		p = p[n:]
		return v, true
	}
	str := func() (string, bool) {
		l, ok := uv()
		if !ok || uint64(len(p)) < l {
			return "", false
		}
		s := string(p[:l])
		p = p[l:]
		return s, true
	}
	n, ok := uv()
	if !ok {
		return errCodec
	}
	m.Counters = make(map[string]int64, n)
	for range n {
		k, ok := str()
		if !ok {
			return errCodec
		}
		v, w := binary.Varint(p)
		if w <= 0 {
			return errCodec
		}
		p = p[w:]
		m.Counters[k] = v
	}
	if n, ok = uv(); !ok {
		return errCodec
	}
	if n > 0 {
		m.Hists = make(map[string]Hist, n)
	}
	for range n {
		k, ok := str()
		c, ok2 := uv()
		if !ok || !ok2 {
			return errCodec
		}
		h := NewHist()
		for range c {
			i, ok := uv()
			v, ok2 := uv()
			if !ok || !ok2 || i >= uint64(len(h)) {
				return errCodec
			}
			h[i] = v
		}
		m.Hists[k] = h
	}
	if n, ok = uv(); !ok {
		return errCodec
	}
	if n > 0 {
		m.Gauges = make(map[string]float64, n)
	}
	for range n {
		k, ok := str()
		v, ok2 := uv()
		if !ok || !ok2 {
			return errCodec
		}
		m.Gauges[k] = math.Float64frombits(v)
	}
	return nil
}
