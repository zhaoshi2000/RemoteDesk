package monitor

import (
	"testing"
	"time"
)

func TestCollectHistory(t *testing.T) {
	c := New(t.TempDir())
	a := c.Collect()
	time.Sleep(time.Millisecond)
	b := c.Collect()
	if !b.At.After(a.At) || b.Goroutines == 0 {
		t.Fatal("missing real sample")
	}
	h := c.History()
	if len(h) != 2 {
		t.Fatal(len(h))
	}
	h[0].HeapBytes = 999
	if c.History()[0].HeapBytes == 999 {
		t.Fatal("history leaked writable slice")
	}
}
func TestRetentionBound(t *testing.T) {
	c := New(t.TempDir())
	for i := 0; i < 721; i++ {
		c.Collect()
	}
	if len(c.History()) != 720 {
		t.Fatal("unbounded history")
	}
}
