// Package stun implements the bounded IPv4 Binding subset of RFC 5389.
// This discovers an address; it does not authenticate a peer or classify every NAT.
package stun

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"time"
)

const magic uint32 = 0x2112a442

func Request() ([]byte, [12]byte) {
	var tx [12]byte
	if _, e := rand.Read(tx[:]); e != nil {
		panic(e)
	}
	b := make([]byte, 20)
	binary.BigEndian.PutUint16(b, 1)
	binary.BigEndian.PutUint32(b[4:], magic)
	copy(b[8:], tx[:])
	return b, tx
}
func IsSTUN(b []byte) bool { return len(b) >= 20 && binary.BigEndian.Uint32(b[4:]) == magic }
func Response(req []byte, addr *net.UDPAddr) ([]byte, error) {
	if len(req) != 20 || binary.BigEndian.Uint16(req) != 1 || binary.BigEndian.Uint16(req[2:]) != 0 || !IsSTUN(req) {
		return nil, errors.New("unsupported STUN binding request")
	}
	ip := addr.IP.To4()
	if ip == nil || addr.Port < 1 {
		return nil, errors.New("IPv4 source required")
	}
	b := make([]byte, 32)
	binary.BigEndian.PutUint16(b, 0x101)
	binary.BigEndian.PutUint16(b[2:], 12)
	binary.BigEndian.PutUint32(b[4:], magic)
	copy(b[8:], req[8:20])
	binary.BigEndian.PutUint16(b[20:], 0x20)
	binary.BigEndian.PutUint16(b[22:], 8)
	b[25] = 1
	binary.BigEndian.PutUint16(b[26:], uint16(addr.Port)^uint16(magic>>16))
	binary.BigEndian.PutUint32(b[28:], binary.BigEndian.Uint32(ip)^magic)
	return b, nil
}
func ParseResponse(b []byte, tx [12]byte) (*net.UDPAddr, error) {
	if len(b) < 20 || !IsSTUN(b) || binary.BigEndian.Uint16(b) != 0x101 || int(binary.BigEndian.Uint16(b[2:])) != len(b)-20 || !bytes.Equal(tx[:], b[8:20]) {
		return nil, errors.New("invalid STUN response or transaction")
	}
	for off := 20; off+4 <= len(b); {
		kind := binary.BigEndian.Uint16(b[off:])
		n := int(binary.BigEndian.Uint16(b[off+2:]))
		off += 4
		if off+n > len(b) {
			return nil, errors.New("truncated STUN attribute")
		}
		if kind == 0x20 && n == 8 && b[off+1] == 1 {
			ip := make([]byte, 4)
			binary.BigEndian.PutUint32(ip, binary.BigEndian.Uint32(b[off+4:])^magic)
			port := binary.BigEndian.Uint16(b[off+2:]) ^ uint16(magic>>16)
			if port == 0 {
				return nil, errors.New("invalid mapped port")
			}
			return &net.UDPAddr{IP: net.IP(ip), Port: int(port)}, nil
		}
		off += (n + 3) &^ 3
	}
	return nil, errors.New("no IPv4 XOR-MAPPED-ADDRESS")
}
func Serve(ctx context.Context, conn *net.UDPConn) error {
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	type rate struct {
		at time.Time
		n  int
	}
	rates := map[string]rate{}
	buf := make([]byte, 1500)
	for {
		n, addr, e := conn.ReadFromUDP(buf)
		if e != nil {
			if ctx.Err() != nil {
				return nil
			}
			return e
		}
		now := time.Now()
		if len(rates) > 2048 {
			for k, v := range rates {
				if now.Sub(v.at) > time.Second {
					delete(rates, k)
				}
			}
		}
		key := addr.IP.String()
		r, ok := rates[key]
		if !ok && len(rates) >= 4096 {
			continue
		}
		if now.Sub(r.at) > time.Second {
			r = rate{at: now}
		}
		r.n++
		rates[key] = r
		if r.n > 30 {
			continue
		}
		reply, e := Response(buf[:n], addr)
		if e != nil {
			continue
		}
		_, _ = conn.WriteToUDP(reply, addr)
	}
}
