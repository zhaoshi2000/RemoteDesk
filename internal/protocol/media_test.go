package protocol

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestMediaHeaderGoldenRoundTrip(t *testing.T) {
	h := MediaHeader{Channel: 3, Flags: 1, FrameID: 0x01020304, Fragment: 1, FragmentCount: 2, TimestampUS: 0x0102030405060708}
	b, e := h.Marshal([]byte{0xaa, 0xbb})
	if e != nil {
		t.Fatal(e)
	}
	const golden = "52444d3101030100010203040001000201020304050607080002aabb"
	if hex.EncodeToString(b) != golden {
		t.Fatalf("wire contract changed: %x", b)
	}
	got, p, e := ParseMedia(b)
	if e != nil || got != h || !bytes.Equal(p, []byte{0xaa, 0xbb}) {
		t.Fatal("media round trip")
	}
}
func TestMediaBounds(t *testing.T) {
	for _, h := range []MediaHeader{{Channel: 3, FragmentCount: 0}, {Channel: 3, Fragment: 2, FragmentCount: 2}, {Channel: 255, FragmentCount: 1}, {Channel: 3, Flags: 4, FragmentCount: 1}} {
		if _, e := h.Marshal(nil); e == nil {
			t.Fatal("invalid media header accepted")
		}
	}
	h := MediaHeader{Channel: 3, FragmentCount: 1}
	b, _ := h.Marshal([]byte("x"))
	for n := 0; n < len(b); n++ {
		if _, _, e := ParseMedia(b[:n]); e == nil {
			t.Fatal("truncation accepted")
		}
	}
	if _, e := h.Marshal(make([]byte, MaxMediaPayload+1)); e == nil {
		t.Fatal("oversized payload accepted")
	}
}
func FuzzParseMedia(f *testing.F) {
	b, _ := (MediaHeader{Channel: 3, FragmentCount: 1}).Marshal([]byte("frame"))
	f.Add(b)
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, b []byte) { _, _, _ = ParseMedia(b) })
}
