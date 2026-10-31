package spec

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The spec changed but `make gen` was not run.
func TestGeneratedCodeIsFresh(t *testing.T) {
	sum := sha256.Sum256(YAML)
	want := hex.EncodeToString(sum[:])
	if GeneratedHash != want {
		t.Fatal("operations_gen.go is older than openapi.yaml: run make gen")
	}
	f, err := os.Open(filepath.Join("..", "..", "frontend", "src", "lib", "api", "schema.d.ts"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	line, _ := bufio.NewReader(f).ReadString('\n')
	if strings.TrimSpace(strings.TrimPrefix(line, "// spec sha256:")) != want {
		t.Fatal("frontend schema.d.ts is older than openapi.yaml: run make gen")
	}
	ops, _ := Operations()
	if len(ops) != len(Ops) {
		t.Fatalf("generated %d operations, spec has %d", len(Ops), len(ops))
	}
}
