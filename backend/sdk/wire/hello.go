package wire

// Hello is exchanged by both sides right after connecting.
type Hello struct {
	Proto   string `cbor:"1,keyasint"`
	Version int    `cbor:"2,keyasint"`
}

// handshake is filled in with version negotiation in the next step.
func (c *Conn) handshake(self Hello) error {
	c.self = self
	return nil
}
