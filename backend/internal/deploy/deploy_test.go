package deploy

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func buildBot(t *testing.T, goos string) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "bot")
	cmd := exec.Command("go", "build", "-o", out, "../runner/testdata/testbot")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS="+goos, "GOARCH=amd64")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	data, _ := os.ReadFile(out)
	return data
}

func TestCheck(t *testing.T) {
	linux := buildBot(t, "linux")
	bi, err := Check(bytes.NewReader(linux), int64(len(linux)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(bi.GoVersion, "go1.") {
		t.Fatalf("build info = %+v", bi)
	}
	win := buildBot(t, "windows")
	if _, err := Check(bytes.NewReader(win), int64(len(win))); err == nil || !strings.Contains(err.Error(), "not an ELF") {
		t.Fatalf("windows exe: %v", err)
	}
	if _, err := Check(bytes.NewReader(linux), MaxBinary+1); err == nil {
		t.Fatal("size limit")
	}
	// A dynamically linked system binary, when there is one (Linux).
	if dyn, err := os.ReadFile("/bin/ls"); err == nil {
		if _, err := Check(bytes.NewReader(dyn), int64(len(dyn))); err == nil || !strings.Contains(err.Error(), "CGO_ENABLED=0") {
			t.Fatalf("dynamic binary: %v", err)
		}
	}
}

func TestStoreUploadAndPrune(t *testing.T) {
	root := t.TempDir()
	s := &Store{Dir: func(int64) string { return root }}
	bin := buildBot(t, "linux")
	upload := func(version string) (Release, error) {
		id, err := s.Begin(1, version, "upload")
		if err != nil {
			return Release{}, err
		}
		for off := 0; off < len(bin); off += 100 << 10 {
			if err := s.Write(id, bin[off:min(off+100<<10, len(bin))]); err != nil {
				return Release{}, err
			}
		}
		return s.Finish(id)
	}
	r, err := upload("v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	if r.Size != int64(len(bin)) || len(r.SHA256) != 64 || !s.Exists(1, "v1.0.0") {
		t.Fatalf("release = %+v", r)
	}
	// The same file again is fine; the same version with another file is not.
	if _, err := upload("v1.0.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := upload("bad/version"); err == nil {
		t.Fatal("bad version accepted")
	}
	// Automatic version names.
	auto, err := upload("")
	if err != nil || auto.Version == "" {
		t.Fatalf("auto version: %+v %v", auto, err)
	}
	for _, v := range []string{"v2", "v3", "v4", "v5", "v6", "v7"} {
		if _, err := upload(v); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Prune(1, "v1.0.0", "v2"); err != nil {
		t.Fatal(err)
	}
	rs, _ := s.List(1, "v1.0.0", "v2")
	have := map[string]bool{}
	for _, r := range rs {
		have[r.Version] = true
	}
	if !have["v1.0.0"] || !have["v2"] || !have["v7"] || len(rs) != Keep {
		t.Fatalf("after prune: %v", have)
	}
}
