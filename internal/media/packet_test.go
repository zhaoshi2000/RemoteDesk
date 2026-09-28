package media

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"testing"
)

func TestReplayWindow(t *testing.T) {
	w := Window{}
	for _, n := range []uint64{5, 7, 6, 69, 8} {
		if !w.Accept(n) {
			t.Fatal(n)
		}
	}
	for _, n := range []uint64{0, 5, 7, 69, 8} {
		if w.Accept(n) {
			t.Fatal("replay", n)
		}
	}
}
func TestSignedPath(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	s, _ := SessionBytes("00112233445566778899aabbccddeeff")
	n := [16]byte{7}
	p := SignedPath(s, Challenge, n, priv)
	if !VerifyPath(p, pub) {
		t.Fatal("signature")
	}
	p[25] ^= 1
	if VerifyPath(p, pub) {
		t.Fatal("tampered")
	}
}
func TestRelayMAC(t *testing.T) {
	s, _ := SessionBytes("00112233445566778899aabbccddeeff")
	k := RelayKey("secret")
	p := RelayPacket(s, 1, 0, 3, k, []byte("opaque QUIC"))
	if !VerifyRelay(p, k) {
		t.Fatal("mac")
	}
	p[63] ^= 1
	if VerifyRelay(p, k) {
		t.Fatal("forged payload")
	}
}
func TestDirectBounds(t *testing.T) {
	s, _ := SessionBytes("00112233445566778899aabbccddeeff")
	p, _ := Direct(s, Data, []byte{1, 2, 3})
	id, k, b, e := ParseDirect(p)
	if e != nil || id != "00112233445566778899aabbccddeeff" || k != 0 || !bytes.Equal(b, []byte{1, 2, 3}) {
		t.Fatal("parse")
	}
	if _, e := Direct(s, Data, make([]byte, MaxDatagram+1)); e == nil {
		t.Fatal("size")
	}
}
