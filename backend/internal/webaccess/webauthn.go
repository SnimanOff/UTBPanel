package webaccess

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"

	"github.com/fxamacker/cbor/v2"
)

// WebAuthn without attestation: trust comes from the four-word code the
// owner compares, as with gate. Only ES256 and RS256 keys are accepted.

// COSE algorithms.
const (
	algES256 = -7
	algRS256 = -257
)

const (
	flagUP = 0x01
	flagUV = 0x04
	flagAT = 0x40
)

var b64 = base64.RawURLEncoding

// RP is the relying party: the domain and the exact origin of the page.
type RP struct {
	ID     string // domain
	Origin string // https://domain[:port]
}

type clientData struct {
	Type      string `json:"type"`
	Challenge string `json:"challenge"`
	Origin    string `json:"origin"`
}

func (rp RP) checkClientData(raw []byte, typ string, challenge []byte) error {
	var cd clientData
	if err := json.Unmarshal(raw, &cd); err != nil {
		return fmt.Errorf("clientDataJSON: %w", err)
	}
	if cd.Type != typ {
		return fmt.Errorf("clientDataJSON: type %q, want %q", cd.Type, typ)
	}
	got, err := b64.DecodeString(cd.Challenge)
	if err != nil || !bytes.Equal(got, challenge) {
		return errors.New("clientDataJSON: challenge does not match")
	}
	if cd.Origin != rp.Origin {
		return fmt.Errorf("clientDataJSON: origin %q, want %q", cd.Origin, rp.Origin)
	}
	return nil
}

type authData struct {
	flags   byte
	count   uint32
	credID  []byte
	coseKey []byte
}

func (rp RP) parseAuthData(b []byte) (authData, error) {
	var a authData
	if len(b) < 37 {
		return a, errors.New("authenticatorData too short")
	}
	want := sha256.Sum256([]byte(rp.ID))
	if !bytes.Equal(b[:32], want[:]) {
		return a, errors.New("authenticatorData: rpIdHash does not match the domain")
	}
	a.flags = b[32]
	a.count = binary.BigEndian.Uint32(b[33:37])
	if a.flags&flagUP == 0 || a.flags&flagUV == 0 {
		return a, errors.New("authenticatorData: the user was not verified (Face ID, fingerprint or PIN)")
	}
	if a.flags&flagAT == 0 {
		return a, nil
	}
	rest := b[37:]
	if len(rest) < 18 {
		return a, errors.New("authenticatorData: short attested credential data")
	}
	n := int(binary.BigEndian.Uint16(rest[16:18]))
	rest = rest[18:]
	if len(rest) < n {
		return a, errors.New("authenticatorData: short credential id")
	}
	a.credID, rest = rest[:n], rest[n:]
	var raw cbor.RawMessage
	if _, err := cbor.UnmarshalFirst(rest, &raw); err != nil {
		return a, fmt.Errorf("credential public key: %w", err)
	}
	a.coseKey = raw
	return a, nil
}

func coseInt(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case uint64:
		if x > math.MaxInt64 {
			return 0, false
		}
		return int64(x), true
	}
	return 0, false
}

// parseCOSE turns a COSE key into SPKI DER and its algorithm.
func parseCOSE(raw []byte) ([]byte, int64, error) {
	var m map[int64]any
	if err := cbor.Unmarshal(raw, &m); err != nil {
		return nil, 0, err
	}
	kty, _ := coseInt(m[1])
	alg, _ := coseInt(m[3])
	var pub crypto.PublicKey
	switch {
	case kty == 2 && alg == algES256:
		crv, _ := coseInt(m[-1])
		x, _ := m[-2].([]byte)
		y, _ := m[-3].([]byte)
		if crv != 1 || len(x) != 32 || len(y) != 32 {
			return nil, 0, errors.New("unsupported EC key: only P-256")
		}
		k, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), append(append([]byte{4}, x...), y...))
		if err != nil {
			return nil, 0, err
		}
		pub = k
	case kty == 3 && alg == algRS256:
		n, _ := m[-1].([]byte)
		e, _ := m[-2].([]byte)
		if len(n) < 256 || len(e) == 0 || len(e) > 4 {
			return nil, 0, errors.New("unsupported RSA key")
		}
		pub = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	default:
		return nil, 0, fmt.Errorf("unsupported key type %d, algorithm %d: only ES256 and RS256", kty, alg)
	}
	der, err := x509.MarshalPKIXPublicKey(pub)
	return der, alg, err
}

// Registration is what the browser returns from navigator.credentials.create.
type Registration struct {
	ID                string `json:"id"`
	ClientDataJSON    string `json:"client_data_json"`
	AttestationObject string `json:"attestation_object"`
}

// Credential is a verified new passkey.
type Credential struct {
	ID        []byte
	PublicKey []byte // SPKI DER
	Alg       int64
	Count     uint32
}

// VerifyRegistration checks a registration against the challenge.
func (rp RP) VerifyRegistration(in Registration, challenge []byte) (Credential, error) {
	var c Credential
	cd, err := b64.DecodeString(in.ClientDataJSON)
	if err != nil {
		return c, errors.New("client_data_json: bad base64url")
	}
	if err := rp.checkClientData(cd, "webauthn.create", challenge); err != nil {
		return c, err
	}
	ao, err := b64.DecodeString(in.AttestationObject)
	if err != nil {
		return c, errors.New("attestation_object: bad base64url")
	}
	var att struct {
		Fmt      string `cbor:"fmt"`
		AuthData []byte `cbor:"authData"`
	}
	if err := cbor.Unmarshal(ao, &att); err != nil {
		return c, fmt.Errorf("attestation_object: %w", err)
	}
	a, err := rp.parseAuthData(att.AuthData)
	if err != nil {
		return c, err
	}
	if a.flags&flagAT == 0 || len(a.credID) == 0 {
		return c, errors.New("authenticatorData: no credential")
	}
	if id, err := b64.DecodeString(in.ID); err != nil || !bytes.Equal(id, a.credID) {
		return c, errors.New("credential id does not match")
	}
	c.PublicKey, c.Alg, err = parseCOSE(a.coseKey)
	if err != nil {
		return c, err
	}
	c.ID, c.Count = a.credID, a.count
	return c, nil
}

// Assertion is what the browser returns from navigator.credentials.get.
type Assertion struct {
	ID                string `json:"id"`
	ClientDataJSON    string `json:"client_data_json"`
	AuthenticatorData string `json:"authenticator_data"`
	Signature         string `json:"signature"`
}

// VerifyAssertion checks a sign-in with a stored key; it returns the new
// signature counter.
func (rp RP) VerifyAssertion(in Assertion, challenge, spki []byte, alg int64, stored uint32) (uint32, error) {
	cd, err1 := b64.DecodeString(in.ClientDataJSON)
	ad, err2 := b64.DecodeString(in.AuthenticatorData)
	sig, err3 := b64.DecodeString(in.Signature)
	if err := errors.Join(err1, err2, err3); err != nil {
		return 0, errors.New("assertion: bad base64url")
	}
	if err := rp.checkClientData(cd, "webauthn.get", challenge); err != nil {
		return 0, err
	}
	a, err := rp.parseAuthData(ad)
	if err != nil {
		return 0, err
	}
	pub, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		return 0, err
	}
	h := sha256.Sum256(cd)
	digest := sha256.Sum256(append(append([]byte{}, ad...), h[:]...))
	switch alg {
	case algES256:
		k, ok := pub.(*ecdsa.PublicKey)
		if !ok || !ecdsa.VerifyASN1(k, digest[:], sig) {
			return 0, errors.New("bad signature")
		}
	case algRS256:
		k, ok := pub.(*rsa.PublicKey)
		if !ok || rsa.VerifyPKCS1v15(k, crypto.SHA256, digest[:], sig) != nil {
			return 0, errors.New("bad signature")
		}
	default:
		return 0, errors.New("unsupported algorithm")
	}
	// A counter that does not grow means a cloned key; passkeys synced
	// between devices always send 0.
	if (a.count != 0 || stored != 0) && a.count <= stored {
		return 0, errors.New("signature counter went back: the key may be cloned")
	}
	return a.count, nil
}
