// Package bridge forwards an opaque byte stream. It never interprets desktop or SSH content.
package bridge

import (
	"context"
	"errors"
	"io"
	"net"
)

type result struct {
	direction int
	n         int64
	err       error
}

func Pipe(ctx context.Context, a, b net.Conn, limit int64) (int64, int64, error) {
	defer a.Close()
	defer b.Close()
	stop := context.AfterFunc(ctx, func() { a.Close(); b.Close() })
	defer stop()
	done := make(chan result, 2)
	copyOne := func(dir int, dst, src net.Conn) {
		var r io.Reader = src
		if limit > 0 {
			r = io.LimitReader(src, limit+1)
		}
		n, e := io.CopyBuffer(dst, r, make([]byte, 32<<10))
		if limit > 0 && n > limit {
			e = errors.New("session byte quota exceeded")
		}
		if e != nil {
			a.Close()
			b.Close()
		} else if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
		done <- result{dir, n, e}
	}
	go copyOne(0, b, a)
	go copyOne(1, a, b)
	r1, r2 := <-done, <-done
	var n [2]int64
	n[r1.direction] = r1.n
	n[r2.direction] = r2.n
	if r1.err != nil {
		return n[0], n[1], r1.err
	}
	return n[0], n[1], r2.err
}
