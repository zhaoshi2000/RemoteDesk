package media

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
)

type Datagram struct {
	Payload []byte
	Address *net.UDPAddr
}
type BrokerConfig struct {
	Session    string
	Side       byte
	Private    ed25519.PrivateKey
	Peer       ed25519.PublicKey
	Candidates []string
	Ticket     string
	RelayUDP   *net.UDPAddr
	Relay      func(context.Context) (net.Conn, error)
	Send       func([]byte, *net.UDPAddr) error
	RelayOnly  bool
}

// Broker changes only the OUTER packet route. Inner QUIC peer addresses remain stable
// on loopback, preserving TLS keys, streams and the connection during direct/relay changes.
// Native QUIC is still responsible for authentication, congestion control and retransmission.
type Broker struct {
	cfg     BrokerConfig
	session [16]byte
	local   *net.UDPConn
	input   chan Datagram
	native  chan *net.UDPAddr
	path    chan string
}

func NewBroker(c BrokerConfig) (*Broker, error) {
	s, e := SessionBytes(c.Session)
	if e != nil {
		return nil, e
	}
	if c.Side > 1 || len(c.Private) != ed25519.PrivateKeySize || len(c.Peer) != ed25519.PublicKeySize || c.Send == nil {
		return nil, errors.New("invalid broker configuration")
	}
	local, e := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if e != nil {
		return nil, e
	}
	_ = local.SetReadBuffer(2 << 20)
	_ = local.SetWriteBuffer(2 << 20)
	return &Broker{c, s, local, make(chan Datagram, 256), make(chan *net.UDPAddr, 1), make(chan string, 8)}, nil
}
func (b *Broker) SessionID() string    { return b.cfg.Session }
func (b *Broker) Port() int            { return b.local.LocalAddr().(*net.UDPAddr).Port }
func (b *Broker) Close() error         { return b.local.Close() }
func (b *Broker) Paths() <-chan string { return b.path }
func (b *Broker) SetNativePort(port int) error {
	if port < 1 || port > 65535 {
		return errors.New("native QUIC port out of range")
	}
	select {
	case b.native <- &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}:
		return nil
	default:
		return errors.New("native endpoint already set")
	}
}
func (b *Broker) Inject(p []byte, a *net.UDPAddr) {
	if len(p) > 2048 {
		return
	}
	d := Datagram{append([]byte(nil), p...), a}
	select {
	case b.input <- d:
	default: /* bounded media queue drops, QUIC handles recovery */
	}
}
func (b *Broker) Run(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	defer b.Close()
	defer close(b.path)
	var wg sync.WaitGroup
	defer func() { cancel(); b.local.Close(); wg.Wait() }()
	stop := context.AfterFunc(ctx, func() { b.local.Close() })
	defer stop()
	localInput := make(chan Datagram, 256)
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 2048)
		for {
			n, a, e := b.local.ReadFromUDP(buf)
			if e != nil {
				return
			}
			if n > MaxDatagram {
				continue
			}
			select {
			case localInput <- Datagram{append([]byte(nil), buf[:n]...), a}:
			case <-ctx.Done():
				return
			}
		}
	}()
	relayIn := make(chan []byte, 256)
	relayOut := make(chan []byte, 256)
	relayErr := make(chan error, 1)
	wg.Add(1)
	go func() { defer wg.Done(); b.relayLoop(ctx, relayIn, relayOut, relayErr) }()
	var native, active *net.UDPAddr
	var activeAt, relayAt time.Time
	var mode string
	var seq uint64
	window := Window{}
	key := RelayKey(b.cfg.Ticket)
	nonce := [16]byte{}
	_, _ = rand.Read(nonce[:])
	ticker := time.NewTicker(750 * time.Millisecond)
	defer ticker.Stop()
	candidates := []*net.UDPAddr{}
	for _, a := range b.cfg.Candidates {
		if addr, e := net.ResolveUDPAddr("udp4", a); e == nil {
			candidates = append(candidates, addr)
		}
	}
	announce := func(m string) {
		if m == mode {
			return
		}
		mode = m
		slog.Info("media route changed", "session", b.cfg.Session, "path", m)
		select {
		case b.path <- m:
		default:
		}
	}
	sendLocal := func(p []byte) {
		if native != nil {
			_, _ = b.local.WriteToUDP(p, native)
		}
	}
	challenge := func() {
		_, _ = rand.Read(nonce[:])
		if !b.cfg.RelayOnly {
			p := SignedPath(b.session, Challenge, nonce, b.cfg.Private)
			for _, a := range candidates {
				_ = b.cfg.Send(p, a)
			}
			if active != nil {
				_ = b.cfg.Send(p, active)
			}
		}
		if b.cfg.RelayUDP != nil {
			seq++
			_ = b.cfg.Send(RelayPacket(b.session, b.cfg.Side, 1, seq, key, nil), b.cfg.RelayUDP)
		}
	}
	challenge()
	for {
		select {
		case <-ctx.Done():
			return nil
		case e := <-relayErr:
			if ctx.Err() == nil {
				slog.Debug("media TCP relay ended", "error", e)
			} // Direct/UDP relay may remain usable.
		case a := <-b.native:
			native = a
		case <-ticker.C:
			challenge()
		case d := <-localInput:
			if native == nil || !native.IP.Equal(d.Address.IP) || native.Port != d.Address.Port {
				continue
			}
			if active != nil && time.Since(activeAt) < 2250*time.Millisecond && !b.cfg.RelayOnly {
				announce("Direct P2P / QUIC")
				p, _ := Direct(b.session, Data, d.Payload)
				_ = b.cfg.Send(p, active)
			} else if b.cfg.RelayUDP != nil && time.Since(relayAt) < 2250*time.Millisecond {
				announce("Relay UDP / end-to-end QUIC")
				seq++
				_ = b.cfg.Send(RelayPacket(b.session, b.cfg.Side, 0, seq, key, d.Payload), b.cfg.RelayUDP)
			} else {
				announce("Relay TLS/TCP fallback / inner QUIC")
				select {
				case relayOut <- d.Payload:
				default:
				}
			}
		case p := <-relayIn:
			sendLocal(p)
		case d := <-b.input:
			p := d.Payload
			if len(p) >= RelayHeader && string(p[:4]) == "RDR2" {
				if b.cfg.RelayUDP == nil || !sameAddr(d.Address, b.cfg.RelayUDP) || !bytes.Equal(p[4:20], b.session[:]) || p[20] != b.cfg.Side || p[21] < 2 || !VerifyRelay(p, key) || !window.Accept(binary.BigEndian.Uint64(p[22:30])) {
					continue
				}
				relayAt = time.Now()
				if p[21] == 2 {
					sendLocal(p[RelayHeader:])
				}
				continue
			}
			sid, kind, payload, e := ParseDirect(p)
			if e != nil || sid != b.cfg.Session || b.cfg.RelayOnly {
				continue
			}
			switch kind {
			case Data:
				if active != nil && sameAddr(active, d.Address) {
					sendLocal(payload)
				}
			case Challenge:
				if VerifyPath(p, b.cfg.Peer) {
					var n [16]byte
					copy(n[:], payload[:16])
					_ = b.cfg.Send(SignedPath(b.session, Response, n, b.cfg.Private), d.Address)
				}
			case Response:
				if VerifyPath(p, b.cfg.Peer) && bytes.Equal(payload[:16], nonce[:]) {
					active = d.Address
					activeAt = time.Now()
				}
			}
		}
	}
}
func sameAddr(a, b *net.UDPAddr) bool {
	return a != nil && b != nil && a.Port == b.Port && a.IP.Equal(b.IP)
}
func (b *Broker) relayLoop(ctx context.Context, in chan<- []byte, out <-chan []byte, errs chan<- error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if b.cfg.Relay == nil {
		return
	}
	c, e := b.cfg.Relay(ctx)
	if e != nil {
		select {
		case errs <- e:
		default:
		}
		return
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-ctx.Done():
				return
			case p := <-out:
				var h [2]byte
				binary.BigEndian.PutUint16(h[:], uint16(len(p)))
				_ = c.SetWriteDeadline(time.Now().Add(5 * time.Second))
				if e := writeAll(c, h[:]); e != nil {
					c.Close()
					return
				}
				if e := writeAll(c, p); e != nil {
					c.Close()
					return
				}
			}
		}
	}()
	defer func() { cancel(); c.Close(); <-done }()
	for {
		var h [2]byte
		if _, e = io.ReadFull(c, h[:]); e != nil {
			break
		}
		n := int(binary.BigEndian.Uint16(h[:]))
		if n == 0 || n > MaxDatagram {
			e = errors.New("invalid relayed datagram size")
			break
		}
		p := make([]byte, n)
		if _, e = io.ReadFull(c, p); e != nil {
			break
		}
		select {
		case in <- p:
		case <-ctx.Done():
			return
		}
	}
	select {
	case errs <- e:
	default:
	}
	// Writer must also stop when the relay reader ends, not leak until process exit.
	// Closing the connection interrupts any pending write; ctx cancellation joins it at Run exit.
}
func writeAll(w io.Writer, p []byte) error {
	for len(p) > 0 {
		n, e := w.Write(p)
		if e != nil {
			return e
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		p = p[n:]
	}
	return nil
}
