// Package media carries opaque end-to-end QUIC packets. It never sees desktop plaintext.
package media

import (
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"time"
)

const MaxDatagram = 1472
const DirectHeader = 21
const RelayHeader = 62
const (
	Data      byte = 0
	Challenge byte = 1
	Response  byte = 2
)

type Window struct {
	Highest uint64
	Bits    uint64
}

func (w *Window) Accept(n uint64) bool {
	if n == 0 {
		return false
	}
	if n > w.Highest {
		d := n - w.Highest
		if d >= 64 {
			w.Bits = 1
		} else {
			w.Bits = (w.Bits << d) | 1
		}
		w.Highest = n
		return true
	}
	d := w.Highest - n
	if d >= 64 || w.Bits&(uint64(1)<<d) != 0 {
		return false
	}
	w.Bits |= uint64(1) << d
	return true
}
func SessionBytes(id string) ([16]byte, error) {
	var s [16]byte
	b, e := hex.DecodeString(id)
	if e != nil || len(b) != 16 {
		return s, errors.New("invalid media session")
	}
	copy(s[:], b)
	return s, nil
}
func Direct(session [16]byte, kind byte, payload []byte) ([]byte, error) {
	if len(payload) > MaxDatagram || kind > Response {
		return nil, errors.New("invalid media packet")
	}
	p := make([]byte, DirectHeader+len(payload))
	copy(p, "RDM2")
	copy(p[4:20], session[:])
	p[20] = kind
	copy(p[21:], payload)
	return p, nil
}
func ParseDirect(p []byte) (id string, kind byte, payload []byte, err error) {
	if len(p) < DirectHeader || len(p) > DirectHeader+MaxDatagram || string(p[:4]) != "RDM2" || p[20] > Response {
		err = errors.New("invalid direct envelope")
		return
	}
	return hex.EncodeToString(p[4:20]), p[20], p[21:], nil
}
func SignedPath(s [16]byte, kind byte, nonce [16]byte, key ed25519.PrivateKey) []byte {
	payload := make([]byte, 24)
	copy(payload, nonce[:])
	binary.BigEndian.PutUint64(payload[16:], uint64(time.Now().Unix()))
	p, _ := Direct(s, kind, payload)
	return append(p, ed25519.Sign(key, p)...)
}
func VerifyPath(p []byte, key ed25519.PublicKey) bool {
	if len(p) != DirectHeader+24+ed25519.SignatureSize || p[20] < Challenge || p[20] > Response {
		return false
	}
	t := int64(binary.BigEndian.Uint64(p[37:45]))
	d := time.Now().Unix() - t
	return d >= -10 && d <= 10 && ed25519.Verify(key, p[:45], p[45:])
}
func RelayKey(ticket string) [32]byte { return sha256.Sum256([]byte(ticket)) }

// Relay envelope: magic, session, side, kind, monotonic sequence, HMAC-SHA256, QUIC payload.
// kind 0=data, 1=keepalive, 2=server-to-endpoint data, 3=server acknowledgement.
func RelayPacket(s [16]byte, side, kind byte, seq uint64, key [32]byte, payload []byte) []byte {
	p := make([]byte, RelayHeader+len(payload))
	copy(p, "RDR2")
	copy(p[4:20], s[:])
	p[20] = side
	p[21] = kind
	binary.BigEndian.PutUint64(p[22:30], seq)
	copy(p[62:], payload)
	h := hmac.New(sha256.New, key[:])
	h.Write(p[:30])
	h.Write(payload)
	copy(p[30:62], h.Sum(nil))
	return p
}
func VerifyRelay(p []byte, key [32]byte) bool {
	if len(p) < RelayHeader || len(p) > RelayHeader+MaxDatagram || string(p[:4]) != "RDR2" || p[20] > 1 || p[21] > 3 {
		return false
	}
	h := hmac.New(sha256.New, key[:])
	h.Write(p[:30])
	h.Write(p[62:])
	return hmac.Equal(p[30:62], h.Sum(nil))
}
