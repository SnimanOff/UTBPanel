package backup

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

const encryptedBlock = 64 << 10

var fullMagic = []byte("UTBPBACKUP\x01")

func backupCipher(key, header []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("master key must have 32 bytes")
	}
	derived, err := hkdf.Key(sha256.New, key, header[len(fullMagic):], "UTBPanel full backup v1", 32)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(derived)
	clear(derived)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
func blockNonce(seq uint64) []byte {
	nonce := make([]byte, 12)
	binary.BigEndian.PutUint64(nonce[4:], seq)
	return nonce
}
func blockAAD(header []byte, seq uint64, size uint32) []byte {
	aad := append([]byte(nil), header...)
	aad = binary.BigEndian.AppendUint64(aad, seq)
	return binary.BigEndian.AppendUint32(aad, size)
}
func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(data) {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

type ChunkWriter struct {
	out    io.Writer
	aead   cipher.AEAD
	header []byte
	buffer []byte
	seq    uint64
	closed bool
	err    error
}

// Each file gets a random HKDF salt, so counters never reuse a nonce with the
// same AES key across files. An authenticated empty frame terminates the file.
func NewChunkWriter(out io.Writer, key []byte) (*ChunkWriter, error) {
	header := append([]byte(nil), fullMagic...)
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	header = append(header, salt...)
	aead, err := backupCipher(key, header)
	if err != nil {
		return nil, err
	}
	if err := writeAll(out, header); err != nil {
		return nil, err
	}
	return &ChunkWriter{out: out, aead: aead, header: header, buffer: make([]byte, 0, encryptedBlock)}, nil
}
func (w *ChunkWriter) frame(data []byte) error {
	if w.seq > math.MaxUint32 || (len(data) > 0 && w.seq == math.MaxUint32) {
		return fmt.Errorf("backup exceeds frame limit")
	}
	size := uint32(len(data))
	sealed := w.aead.Seal(nil, blockNonce(w.seq), data, blockAAD(w.header, w.seq, size))
	frame := binary.BigEndian.AppendUint32(nil, size)
	frame = append(frame, sealed...)
	if err := writeAll(w.out, frame); err != nil {
		return err
	}
	w.seq++
	return nil
}
func (w *ChunkWriter) Write(data []byte) (int, error) {
	if w.err != nil {
		return 0, w.err
	}
	if w.closed {
		return 0, errors.New("backup writer closed")
	}
	count := 0
	for len(data) > 0 {
		n := min(encryptedBlock-len(w.buffer), len(data))
		w.buffer = append(w.buffer, data[:n]...)
		data = data[n:]
		count += n
		if len(w.buffer) == encryptedBlock {
			if err := w.frame(w.buffer); err != nil {
				w.err = err
				return count, err
			}
			w.buffer = w.buffer[:0]
		}
	}
	return count, nil
}
func (w *ChunkWriter) Close() error {
	if w.closed {
		return w.err
	}
	w.closed = true
	if w.err != nil {
		return w.err
	}
	if len(w.buffer) > 0 {
		w.err = w.frame(w.buffer)
		clear(w.buffer)
		w.buffer = nil
		if w.err != nil {
			return w.err
		}
	}
	w.err = w.frame(nil)
	return w.err
}

type ChunkReader struct {
	in     io.Reader
	aead   cipher.AEAD
	header []byte
	buffer []byte
	seq    uint64
	done   bool
	err    error
}

func NewChunkReader(in io.Reader, key []byte) (*ChunkReader, error) {
	header := make([]byte, len(fullMagic)+32)
	if _, err := io.ReadFull(in, header); err != nil {
		return nil, err
	}
	if !bytes.Equal(header[:len(fullMagic)], fullMagic) {
		return nil, fmt.Errorf("unsupported backup format")
	}
	aead, err := backupCipher(key, header)
	if err != nil {
		return nil, err
	}
	return &ChunkReader{in: in, aead: aead, header: header}, nil
}
func (r *ChunkReader) Read(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	if len(r.buffer) > 0 {
		n := copy(data, r.buffer)
		r.buffer = r.buffer[n:]
		return n, nil
	}
	if r.err != nil {
		return 0, r.err
	}
	if r.done {
		return 0, io.EOF
	}
	if r.seq > math.MaxUint32 {
		r.err = fmt.Errorf("backup exceeds frame limit")
		return 0, r.err
	}
	var encoded [4]byte
	if _, err := io.ReadFull(r.in, encoded[:]); err != nil {
		r.err = fmt.Errorf("backup is truncated: %w", err)
		return 0, r.err
	}
	size := binary.BigEndian.Uint32(encoded[:])
	if size > encryptedBlock {
		r.err = fmt.Errorf("invalid backup frame size")
		return 0, r.err
	}
	sealed := make([]byte, int(size)+r.aead.Overhead())
	if _, err := io.ReadFull(r.in, sealed); err != nil {
		r.err = fmt.Errorf("backup is truncated: %w", err)
		return 0, r.err
	}
	plain, err := r.aead.Open(nil, blockNonce(r.seq), sealed, blockAAD(r.header, r.seq, size))
	if err != nil {
		r.err = fmt.Errorf("wrong master key or corrupted backup: %w", err)
		return 0, r.err
	}
	r.seq++
	if size == 0 {
		var extra [1]byte
		n, err := io.ReadFull(r.in, extra[:])
		if n != 0 || err != io.EOF {
			r.err = fmt.Errorf("data after backup terminator")
			return 0, r.err
		}
		r.done = true
		return 0, io.EOF
	}
	r.buffer = plain
	return r.Read(data)
}
