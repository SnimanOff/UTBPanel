//go:build windows

package main

import (
	"encoding/binary"
	"testing"
)

func TestIcon(t *testing.T) {
	for _, down := range []bool{false, true} {
		ico := icon(down)
		if binary.LittleEndian.Uint16(ico[2:]) != 1 || binary.LittleEndian.Uint16(ico[4:]) != 1 {
			t.Fatal("not an ICO with one image")
		}
		size := binary.LittleEndian.Uint32(ico[14:])
		off := binary.LittleEndian.Uint32(ico[18:])
		if int(off+size) != len(ico) || size != 40+32*32*4+32*32/8 {
			t.Fatalf("size %d offset %d total %d", size, off, len(ico))
		}
	}
}
