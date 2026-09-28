package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

func keys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	p, k, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	return p, k
}
func TestRequestSignatureBindsEveryField(t *testing.T) {
	p, k := keys(t)
	base := CanonicalRequest("POST", "/v1/test?q=1", "123", "nonce", PublicString(p), []byte("body"))
	sig := Sign(k, base)
	if !Verify(p, base, sig) {
		t.Fatal("valid signature rejected")
	}
	for _, b := range [][]byte{CanonicalRequest("GET", "/v1/test?q=1", "123", "nonce", PublicString(p), []byte("body")), CanonicalRequest("POST", "/v1/test?q=2", "123", "nonce", PublicString(p), []byte("body")), CanonicalRequest("POST", "/v1/test?q=1", "124", "nonce", PublicString(p), []byte("body")), CanonicalRequest("POST", "/v1/test?q=1", "123", "nonce2", PublicString(p), []byte("body")), CanonicalRequest("POST", "/v1/test?q=1", "123", "nonce", PublicString(p), []byte("tampered"))} {
		if Verify(p, b, sig) {
			t.Fatal("tampering accepted")
		}
	}
}
func TestCandidatesRequireSignedIdentityAndFreshness(t *testing.T) {
	p, k := keys(t)
	c := CandidateSet{DeviceID: DeviceID(p), TCP: []string{"127.0.0.1:1234"}, UDP: []string{"192.168.1.2:1234"}, IssuedAt: time.Now().Unix()}
	c.Sign(k)
	if !c.Verify(p) {
		t.Fatal("valid candidate rejected")
	}
	c.TCP = []string{"8.8.8.8:22"}
	if c.Verify(p) {
		t.Fatal("unsigned target injection accepted")
	}
	c.IssuedAt = time.Now().Add(-2 * time.Minute).Unix()
	c.Sign(k)
	if c.Verify(p) {
		t.Fatal("expired candidate accepted")
	}
}
func TestIntentAndProbe(t *testing.T) {
	p, k := keys(t)
	other, _ := keys(t)
	i := Intent{From: DeviceID(p), To: DeviceID(other), Kind: "ssh", Nonce: RandomHex(16), IssuedAt: time.Now().Unix()}
	i.Sign(k)
	if !i.Verify(p) {
		t.Fatal("valid intent rejected")
	}
	i.Kind = "shell"
	i.Sign(k)
	if i.Verify(p) {
		t.Fatal("unsupported capability accepted")
	}
	pr := ProbePacket{Session: RandomHex(16), From: DeviceID(p), To: DeviceID(other), Kind: "ping", Nonce: RandomHex(16), IssuedAt: time.Now().Unix()}
	pr.Sign(k)
	if !pr.Verify(p) {
		t.Fatal("valid probe rejected")
	}
	pr.Nonce = RandomHex(16)
	if pr.Verify(p) {
		t.Fatal("modified probe accepted")
	}
}
func TestAddressValidation(t *testing.T) {
	for _, a := range []string{"0.0.0.0:1234", "255.255.255.255:1", "224.0.0.1:9000", "example.com:22", "127.0.0.1:0", "127.0.0.1:65536", "garbage"} {
		if ValidAddress(a) {
			t.Errorf("accepted invalid address %s", a)
		}
	}
	for _, a := range []string{"127.0.0.1:22", "192.168.1.2:40000", "[::1]:1234"} {
		if !ValidAddress(a) {
			t.Errorf("rejected valid address %s", a)
		}
	}
}
func TestPublicKeyAndID(t *testing.T) {
	p, _ := keys(t)
	got, e := ParsePublic(PublicString(p))
	if e != nil || !p.Equal(got) {
		t.Fatal("public key round trip")
	}
	if len(DeviceID(p)) != 12 {
		t.Fatal("ID must be twelve decimal digits")
	}
	if _, e = ParsePublic("AAAA"); e == nil {
		t.Fatal("invalid public key accepted")
	}
}
