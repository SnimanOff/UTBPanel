package gatefile

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func sample() *Block {
	return &Block{
		Servers:       []string{"203.0.113.10:443"},
		CAFingerprint: bytes.Repeat([]byte{1}, 32),
		EnrollCert:    []byte("cert"),
		EnrollKey:     []byte("key"),
		KeyName:       "home-pc",
		Kind:          "owner",
	}
}

func TestWriteRead(t *testing.T) {
	exe := bytes.Repeat([]byte("MZ fake exe "), 1000)
	var out bytes.Buffer
	n, err := Write(&out, bytes.NewReader(exe), sample())
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(out.Len()) || !bytes.HasPrefix(out.Bytes(), exe) {
		t.Fatal("exe not copied as is")
	}
	path := filepath.Join(t.TempDir(), "gate-home-pc.exe")
	os.WriteFile(path, out.Bytes(), 0o755)
	b, err := ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(b, sample()) {
		t.Fatalf("block = %+v", b)
	}
}

func TestNoBlock(t *testing.T) {
	if _, err := DecodeBytes([]byte("MZ just an exe without data")); !errors.Is(err, ErrNoBlock) {
		t.Fatalf("got %v", err)
	}
	if _, err := DecodeBytes(nil); !errors.Is(err, ErrNoBlock) {
		t.Fatalf("empty: %v", err)
	}
}

func TestDamagedBlock(t *testing.T) {
	enc, _ := sample().Encode()
	exe := append([]byte("MZ exe"), enc...)

	flipped := bytes.Clone(exe)
	flipped[len("MZ exe")+2] ^= 0xff // inside the CBOR data
	if _, err := DecodeBytes(flipped); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Errorf("flipped data: %v", err)
	}

	badLen := bytes.Clone(exe)
	badLen[len(badLen)-trailer] = 0xff // length far bigger than the file
	if _, err := DecodeBytes(badLen); err == nil || !strings.Contains(err.Error(), "damaged") {
		t.Errorf("bad length: %v", err)
	}
}
