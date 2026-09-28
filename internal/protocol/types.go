// Package protocol defines versioned control-plane messages. No video is JSON encoded.
package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

const Version = 2
const MaxBody = 64 << 10
const ClockWindow = 60 * time.Second

func RandomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}
func PublicString(k ed25519.PublicKey) string { return base64.RawStdEncoding.EncodeToString(k) }
func ParsePublic(s string) (ed25519.PublicKey, error) {
	b, e := base64.RawStdEncoding.DecodeString(s)
	if e != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("invalid Ed25519 public key")
	}
	return ed25519.PublicKey(b), nil
}
func DeviceID(k ed25519.PublicKey) string {
	h := sha256.Sum256(k)
	return fmt.Sprintf("%012d", 100000000000+binary.BigEndian.Uint64(h[:8])%900000000000)
}
func Fingerprint(k ed25519.PublicKey) string {
	h := sha256.Sum256(k)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(h[:])
}
func Sign(k ed25519.PrivateKey, b []byte) string {
	return base64.RawStdEncoding.EncodeToString(ed25519.Sign(k, b))
}
func Verify(k ed25519.PublicKey, b []byte, s string) bool {
	sig, e := base64.RawStdEncoding.DecodeString(s)
	return e == nil && ed25519.Verify(k, b, sig)
}
func Fresh(ts int64, now time.Time) bool {
	d := now.Sub(time.Unix(ts, 0))
	return d <= ClockWindow && d >= -ClockWindow
}
func ValidNonce(s string) bool { b, e := hex.DecodeString(s); return e == nil && len(b) == 16 }
func CanonicalRequest(method, uri, ts, nonce, key string, body []byte) []byte {
	h := sha256.Sum256(body)
	return []byte(strings.Join([]string{"RemoteDesk-HTTP-v1", method, uri, ts, nonce, key, hex.EncodeToString(h[:])}, "\n"))
}

type Device struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	PublicKey    string `json:"public_key"`
	RegisteredAt int64  `json:"registered_at"`
	Disabled     bool   `json:"disabled"`
	Owner        string `json:"owner,omitempty"`
}
type CandidateSet struct {
	DeviceID  string   `json:"device_id"`
	TCP       []string `json:"tcp"`
	UDP       []string `json:"udp"`
	IssuedAt  int64    `json:"issued_at"`
	Signature string   `json:"signature"`
}

func (c CandidateSet) bytes() []byte {
	c.Signature = ""
	b, _ := json.Marshal(c)
	return append([]byte("RemoteDesk-Candidates-v1\n"), b...)
}
func (c *CandidateSet) Sign(k ed25519.PrivateKey) { c.Signature = Sign(k, c.bytes()) }
func (c CandidateSet) Verify(k ed25519.PublicKey) bool {
	if c.DeviceID != DeviceID(k) || !Fresh(c.IssuedAt, time.Now()) || len(c.TCP) > 16 || len(c.UDP) > 16 {
		return false
	}
	for _, a := range append(append([]string{}, c.TCP...), c.UDP...) {
		if !ValidAddress(a) {
			return false
		}
	}
	return Verify(k, c.bytes(), c.Signature)
}
func ValidAddress(a string) bool {
	host, port, e := net.SplitHostPort(a)
	if e != nil {
		return false
	}
	ip := net.ParseIP(host)
	p, e := strconv.Atoi(port)
	return e == nil && p > 0 && p < 65536 && ip != nil && !ip.IsUnspecified() && !ip.IsMulticast() && !ip.Equal(net.IPv4bcast)
}

type Peer struct {
	Device     Device       `json:"device"`
	Candidates CandidateSet `json:"candidates"`
	Online     bool         `json:"online"`
	LastSeen   int64        `json:"last_seen"`
}
type Intent struct {
	From            string   `json:"from"`
	To              string   `json:"to"`
	Kind            string   `json:"kind"`
	Nonce           string   `json:"nonce"`
	IssuedAt        int64    `json:"issued_at"`
	Signature       string   `json:"signature"`
	MediaCandidates []string `json:"media_candidates,omitempty"`
}

func (i Intent) bytes() []byte {
	i.Signature = ""
	b, _ := json.Marshal(i)
	return append([]byte("RemoteDesk-Intent-v1\n"), b...)
}
func (i *Intent) Sign(k ed25519.PrivateKey) { i.Signature = Sign(k, i.bytes()) }
func (i Intent) Verify(k ed25519.PublicKey) bool {
	if len(i.MediaCandidates) > 16 {
		return false
	}
	for _, a := range i.MediaCandidates {
		if !ValidAddress(a) {
			return false
		}
	}
	return i.From == DeviceID(k) && i.From != i.To && (i.Kind == "ssh" || i.Kind == "probe" || i.Kind == "desktop" || i.Kind == "file") && ValidNonce(i.Nonce) && Fresh(i.IssuedAt, time.Now()) && Verify(k, i.bytes(), i.Signature)
}

type Event struct {
	Seq       uint64 `json:"seq"`
	SessionID string `json:"session_id"`
	Intent    Intent `json:"intent"`
	Peer      Peer   `json:"peer"`
	Ticket    string `json:"ticket,omitempty"`
}
type SessionResponse struct {
	ID        string `json:"id"`
	Ticket    string `json:"ticket,omitempty"`
	ExpiresAt int64  `json:"expires_at"`
}
type ProbeResult struct {
	SessionID string `json:"session_id"`
	Address   string `json:"address"`
	Success   bool   `json:"success"`
}
type SessionStatus struct {
	ID         string `json:"id"`
	Kind       string `json:"kind"`
	From       string `json:"from"`
	To         string `json:"to"`
	AReachable bool   `json:"a_reachable"`
	BReachable bool   `json:"b_reachable"`
	Paired     bool   `json:"paired"`
	Closed     bool   `json:"closed"`
	ExpiresAt  int64  `json:"expires_at"`
}
type ProbePacket struct {
	Session   string `json:"session"`
	From      string `json:"from"`
	To        string `json:"to"`
	Kind      string `json:"kind"`
	Nonce     string `json:"nonce"`
	IssuedAt  int64  `json:"issued_at"`
	Signature string `json:"signature"`
}

func (p ProbePacket) bytes() []byte {
	p.Signature = ""
	b, _ := json.Marshal(p)
	return append([]byte("RemoteDesk-UDP-Probe-v1\n"), b...)
}
func (p *ProbePacket) Sign(k ed25519.PrivateKey) { p.Signature = Sign(k, p.bytes()) }
func (p ProbePacket) Verify(k ed25519.PublicKey) bool {
	return p.From == DeviceID(k) && ValidNonce(p.Session) && ValidNonce(p.Nonce) && (p.Kind == "ping" || p.Kind == "pong") && Fresh(p.IssuedAt, time.Now()) && Verify(k, p.bytes(), p.Signature)
}
