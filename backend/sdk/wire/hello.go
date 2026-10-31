package wire

import (
	"fmt"
	"slices"
)

// Hello is exchanged by both sides right after connecting.
type Hello struct {
	Proto      string   `cbor:"1,keyasint"` // e.g. "core-panel"
	Version    int      `cbor:"2,keyasint"`
	MinVersion int      `cbor:"3,keyasint"` // oldest peer version still supported
	Caps       []string `cbor:"4,keyasint,omitempty"`
	Name       string   `cbor:"5,keyasint,omitempty"` // program and version, for logs
}

// handshake sends our hello, reads the peer's and agrees on the lower
// version. Incompatible peers get an error and the connection is dropped.
func (c *Conn) handshake(self Hello) error {
	if self.Version == 0 {
		self.Version = 1
	}
	if self.MinVersion == 0 {
		self.MinVersion = 1
	}
	c.self = self
	if err := c.send(fHello, 0, self); err != nil {
		return err
	}
	// The hello is the very first frame; read it unbuffered so that nothing
	// after it is consumed before the read loop starts.
	typ, _, payload, err := readFrame(c.nc)
	if err != nil {
		return fmt.Errorf("wire: reading hello: %w", err)
	}
	if typ != fHello {
		return fmt.Errorf("wire: expected hello, got frame type %d", typ)
	}
	var peer Hello
	if err := Unmarshal(payload, &peer); err != nil {
		return fmt.Errorf("wire: bad hello: %w", err)
	}
	if peer.Proto != self.Proto {
		return Errorf("incompatible", "protocol %q, expected %q", peer.Proto, self.Proto)
	}
	v := min(self.Version, peer.Version)
	if v < self.MinVersion {
		return Errorf("incompatible", "%s: peer version %d is older than supported %d", self.Proto, peer.Version, self.MinVersion)
	}
	if v < peer.MinVersion {
		return Errorf("incompatible", "%s: our version %d is older than the peer supports (%d)", self.Proto, self.Version, peer.MinVersion)
	}
	c.info = PeerInfo{Hello: peer, Version: v}
	c.version.Store(int32(v))
	return nil
}

// Peer returns the peer's hello and the negotiated version.
func (c *Conn) Peer() PeerInfo { return c.info }

// Version returns the negotiated protocol version.
func (c *Conn) Version() int { return int(c.version.Load()) }

// Has reports whether both sides support a capability.
func (c *Conn) Has(cap string) bool {
	return slices.Contains(c.info.Hello.Caps, cap) && slices.Contains(c.self.Caps, cap)
}

// PeerHas reports whether the peer announced a capability.
func (c *Conn) PeerHas(cap string) bool { return slices.Contains(c.info.Hello.Caps, cap) }
