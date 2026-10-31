// Package gatefile reads and writes the data block at the end of gate.exe.
// One gate.exe is built with the panel; the panel makes each
// copy unique by appending:
//
//	data (CBOR) | length uint32 | "TGPANEL1"
package gatefile

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/SnimanOff/UTBPanel/backend/internal/wire"
)

// Magic ends every data block.
const Magic = "TGPANEL1"

const trailer = 4 + len(Magic)

// MaxBlock limits the block size when reading.
const MaxBlock = 1 << 20

// ErrNoBlock means the file has no data block.
var ErrNoBlock = errors.New("gatefile: no panel data in this gate.exe; download the key again from the panel")

// Block is what a gate needs to reach and trust its panel.
type Block struct {
	Servers       []string `cbor:"1,keyasint"` // host:port, first is preferred
	CAFingerprint []byte   `cbor:"2,keyasint"` // SHA-256 of the CA SPKI
	EnrollCert    []byte   `cbor:"3,keyasint"` // DER, valid 24 h, only for /enroll
	// EnrollKey is the software key of the enrollment certificate (PKCS#8).
	// It is single-use, lives 24 hours and is useless without the code
	// confirmed in the panel.
	EnrollKey []byte `cbor:"4,keyasint"`
	KeyName   string `cbor:"5,keyasint"`
	Kind      string `cbor:"6,keyasint"` // owner | child
}

// Encode returns the block with its trailer.
func (b *Block) Encode() ([]byte, error) {
	data, err := wire.Marshal(b)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(data)+trailer)
	out = append(out, data...)
	out = binary.BigEndian.AppendUint32(out, uint32(len(data)))
	return append(out, Magic...), nil
}

// Write copies exe to w and appends the block, as a stream.
func Write(w io.Writer, exe io.Reader, b *Block) (int64, error) {
	tail, err := b.Encode()
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(w, exe)
	if err != nil {
		return n, err
	}
	m, err := w.Write(tail)
	return n + int64(m), err
}

// Decode reads a block from the end of r of the given size.
func Decode(r io.ReaderAt, size int64) (*Block, error) {
	if size < int64(trailer) {
		return nil, ErrNoBlock
	}
	var t [trailer]byte
	if _, err := r.ReadAt(t[:], size-int64(trailer)); err != nil {
		return nil, err
	}
	if string(t[4:]) != Magic {
		return nil, ErrNoBlock
	}
	n := int64(binary.BigEndian.Uint32(t[:4]))
	if n == 0 || n > MaxBlock || n > size-int64(trailer) {
		return nil, fmt.Errorf("gatefile: damaged data block (length %d)", n)
	}
	data := make([]byte, n)
	if _, err := r.ReadAt(data, size-int64(trailer)-n); err != nil {
		return nil, err
	}
	var b Block
	if err := wire.Unmarshal(data, &b); err != nil {
		return nil, fmt.Errorf("gatefile: damaged data block: %w", err)
	}
	if len(b.Servers) == 0 || len(b.CAFingerprint) == 0 {
		return nil, errors.New("gatefile: damaged data block: no server or CA")
	}
	return &b, nil
}

// ReadFile reads the block from the end of a file.
func ReadFile(path string) (*Block, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	return Decode(f, fi.Size())
}

// ReadSelf reads the block of the running gate: from gate.dat next to the
// executable if present (for a signed gate.exe), otherwise from
// the end of the executable itself.
func ReadSelf() (*Block, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if b, err := ReadFile(filepath.Join(filepath.Dir(exe), "gate.dat")); err == nil {
		return b, nil
	}
	return ReadFile(exe)
}

// DecodeBytes reads a block from a byte slice.
func DecodeBytes(b []byte) (*Block, error) { return Decode(bytes.NewReader(b), int64(len(b))) }
