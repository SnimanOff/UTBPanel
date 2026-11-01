package backup

import (
	"bytes"
	"io"
	"testing"
)

func TestFullBackupEncryptionAuthenticatesEveryFrameAndEnd(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	plain := bytes.Repeat([]byte("private core and CA data"), 20_000)
	encrypt := func() []byte {
		var out bytes.Buffer
		writer, err := NewChunkWriter(&out, key)
		if err != nil {
			t.Fatal(err)
		}
		for offset := 0; offset < len(plain); offset += 777 {
			if _, err := writer.Write(plain[offset:min(offset+777, len(plain))]); err != nil {
				t.Fatal(err)
			}
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		return out.Bytes()
	}
	encrypted := encrypt()
	if bytes.Equal(encrypted, encrypt()) || bytes.Contains(encrypted, []byte("private core")) {
		t.Fatal("not randomized or plaintext leaked")
	}
	reader, err := NewChunkReader(bytes.NewReader(encrypted), key)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(decoded, plain) {
		t.Fatal("roundtrip", err)
	}
	tampered := append([]byte(nil), encrypted...)
	tampered[len(fullMagic)+32+10] ^= 1
	for name, data := range map[string][]byte{"tampered": tampered, "truncated": encrypted[:len(encrypted)-1], "missing end": encrypted[:len(encrypted)-20], "appended": append(append([]byte(nil), encrypted...), 1)} {
		t.Run(name, func(t *testing.T) {
			reader, err := NewChunkReader(bytes.NewReader(data), key)
			if err == nil {
				_, err = io.Copy(io.Discard, reader)
			}
			if err == nil {
				t.Fatal("corrupt stream accepted")
			}
		})
	}
	wrong := bytes.Repeat([]byte{8}, 32)
	reader, err = NewChunkReader(bytes.NewReader(encrypted), wrong)
	if err == nil {
		_, err = io.ReadAll(reader)
	}
	if err == nil {
		t.Fatal("wrong master key accepted")
	}
}
