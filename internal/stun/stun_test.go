package stun

import (
	"crypto/rand"
	"net"
	"testing"
)

func TestBindingRoundTripAndTransactionValidation(t *testing.T) {
	req, tx := Request()
	addr := &net.UDPAddr{IP: net.ParseIP("203.0.113.8"), Port: 45678}
	resp, e := Response(req, addr)
	if e != nil {
		t.Fatal(e)
	}
	got, e := ParseResponse(resp, tx)
	if e != nil || got.String() != addr.String() {
		t.Fatalf("round trip: %v %v", got, e)
	}
	wrong := tx
	wrong[0] ^= 1
	if _, e = ParseResponse(resp, wrong); e == nil {
		t.Fatal("wrong transaction accepted")
	}
	resp[2] = 0xff
	if _, e = ParseResponse(resp, tx); e == nil {
		t.Fatal("invalid length accepted")
	}
}
func TestRejectMalformedAndIPv6(t *testing.T) {
	req, _ := Request()
	if _, e := Response(req, &net.UDPAddr{IP: net.ParseIP("::1"), Port: 22}); e == nil {
		t.Fatal("unimplemented IPv6 accepted")
	}
	for size := 0; size < 256; size++ {
		data := make([]byte, size)
		_, _ = rand.Read(data)
		_, _ = ParseResponse(data, [12]byte{})
		_, _ = Response(data, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 22})
	}
}
func FuzzParseResponse(f *testing.F) {
	req, tx := Request()
	resp, _ := Response(req, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234})
	f.Add(resp)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) { _, _ = ParseResponse(b, tx) })
}
