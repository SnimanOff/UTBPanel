package wire

import (
	"errors"
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

func normalize(h Hello) Hello {
	if h.Version == 0 {
		h.Version = 1
	}
	if h.MinVersion == 0 {
		h.MinVersion = 1
	}
	return h
}

func (c *Conn) readHello() (Hello, error) {
	// The hello is the very first frame; read it unbuffered so that nothing
	// after it is consumed before the read loop starts.
	typ, _, payload, err := readFrame(c.nc)
	if err != nil {
		return Hello{}, fmt.Errorf("wire: reading hello: %w", err)
	}
	if typ != fHello {
		return Hello{}, fmt.Errorf("wire: expected hello, got frame type %d", typ)
	}
	var peer Hello
	if err := Unmarshal(payload, &peer); err != nil {
		return Hello{}, fmt.Errorf("wire: bad hello: %w", err)
	}
	return peer, nil
}

// clientHandshake sends our hello first, then reads the server's.
func (c *Conn) clientHandshake(self Hello) error {
	self = normalize(self)
	if err := c.send(fHello, 0, self); err != nil {
		return err
	}
	peer, err := c.readHello()
	if err != nil {
		return err
	}
	return c.agree(self, peer)
}

// serverHandshake reads the client's hello and answers with the hello of
// the same protocol, so one socket can serve several protocols.
func (c *Conn) serverHandshake(selves []Hello) error {
	peer, err := c.readHello()
	if err != nil {
		return err
	}
	if len(selves) == 0 {
		return errors.New("wire: server has no protocols")
	}
	self := normalize(selves[0])
	for _, h := range selves {
		if h.Proto == peer.Proto {
			self = normalize(h)
		}
	}
	if err := c.send(fHello, 0, self); err != nil {
		return err
	}
	return c.agree(self, peer)
}

// agree picks the lower of the two versions. Incompatible peers get an
// error and the connection is dropped.
func (c *Conn) agree(self, peer Hello) error {
	c.self = self
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

// Proto returns the protocol of this connection.
func (c *Conn) Proto() string { return c.self.Proto }

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
