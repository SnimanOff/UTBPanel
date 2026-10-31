// Package deploy keeps bot releases and switches versions.
package deploy

import (
	"debug/buildinfo"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"strings"
)

// MaxBinary is the largest accepted bot binary.
var MaxBinary int64 = 100 << 20

// BuildInfo is what the Go toolchain recorded in the binary.
type BuildInfo struct {
	Module    string `cbor:"1,keyasint" json:"module,omitempty"`
	GoVersion string `cbor:"2,keyasint" json:"go_version,omitempty"`
	Version   string `cbor:"3,keyasint" json:"version,omitempty"`
	Revision  string `cbor:"4,keyasint" json:"revision,omitempty"`
	Time      string `cbor:"5,keyasint" json:"vcs_time,omitempty"`
	Modified  bool   `cbor:"6,keyasint" json:"modified,omitempty"`
}

// CheckError explains why a binary was refused.
type CheckError struct{ Msg string }

func (e *CheckError) Error() string { return e.Msg }

func refuse(format string, args ...any) error { return &CheckError{fmt.Sprintf(format, args...)} }

// Check verifies that r is a static linux/amd64 ELF built without cgo and
// returns its Go build info (empty for non-Go binaries).
func Check(r io.ReaderAt, size int64) (BuildInfo, error) {
	var bi BuildInfo
	if size > MaxBinary {
		return bi, refuse("file is %d MB, the limit is %d MB", size>>20, MaxBinary>>20)
	}
	f, err := elf.NewFile(r)
	if err != nil {
		return bi, refuse("not an ELF binary: build it for Linux (GOOS=linux)")
	}
	defer f.Close()
	if f.Machine != elf.EM_X86_64 {
		return bi, refuse("architecture %s, expected amd64 (GOARCH=amd64)", f.Machine)
	}
	for _, p := range f.Progs {
		if p.Type == elf.PT_INTERP {
			return bi, refuse("the binary is dynamically linked: build it with CGO_ENABLED=0")
		}
	}
	if libs, _ := f.ImportedLibraries(); len(libs) > 0 {
		return bi, refuse("the binary needs %s: build it with CGO_ENABLED=0", strings.Join(libs, ", "))
	}
	if info, err := buildinfo.Read(io.NewSectionReader(r, 0, size)); err == nil {
		bi.Module, bi.GoVersion = info.Main.Path, info.GoVersion
		if v := info.Main.Version; v != "" && v != "(devel)" {
			bi.Version = v
		}
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				bi.Revision = s.Value
			case "vcs.time":
				bi.Time = s.Value
			case "vcs.modified":
				bi.Modified = s.Value == "true"
			case "CGO_ENABLED":
				if s.Value == "1" {
					return bi, refuse("built with CGO_ENABLED=1: rebuild with CGO_ENABLED=0")
				}
			}
		}
	}
	return bi, nil
}

// IsCheckError reports whether err is a refusal with a message for the owner.
func IsCheckError(err error) bool {
	var ce *CheckError
	return errors.As(err, &ce)
}
