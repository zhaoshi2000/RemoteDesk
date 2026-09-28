// Package apiclient signs every device request and validates the coordinator's HTTPS certificate.
package apiclient

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"remotedesk.local/remotedesk/internal/identity"
	"remotedesk.local/remotedesk/internal/protocol"
)

type Client struct {
	Identity *identity.Identity
	Config   identity.Config
	HTTP     *http.Client
	TLS      *tls.Config
}
type HTTPError struct {
	Status  int
	Message string
}

func (e *HTTPError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.Status, e.Message) }
func New(id *identity.Identity, c identity.Config) (*Client, error) {
	if e := identity.ValidateConfig(c); e != nil {
		return nil, e
	}
	roots, e := x509.SystemCertPool()
	if e != nil {
		roots = x509.NewCertPool()
	}
	if c.CAFile != "" {
		pem, e := os.ReadFile(c.CAFile)
		if e != nil {
			return nil, e
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("CA file contains no certificates")
		}
	}
	tc := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13, NextProtos: []string{"http/1.1"}}
	transport := &http.Transport{TLSClientConfig: tc, ForceAttemptHTTP2: false, DialContext: (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext, MaxIdleConns: 16, IdleConnTimeout: 60 * time.Second, TLSHandshakeTimeout: 5 * time.Second}
	return &Client{id, c, &http.Client{Transport: transport, Timeout: 25 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("coordinator redirects are not allowed") }}, tc}, nil
}
func (c *Client) Close() { c.HTTP.CloseIdleConnections() }
func (c *Client) Request(ctx context.Context, method, path string, input any, token string) (*http.Request, error) {
	var body []byte
	var e error
	if input != nil {
		body, e = json.Marshal(input)
		if e != nil {
			return nil, e
		}
	}
	r, e := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Config.Server, "/")+path, bytes.NewReader(body))
	if e != nil {
		return nil, e
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonce := protocol.RandomHex(16)
	pub := protocol.PublicString(c.Identity.Public)
	r.Header.Set("X-RD-Key", pub)
	r.Header.Set("X-RD-Time", ts)
	r.Header.Set("X-RD-Nonce", nonce)
	r.Header.Set("X-RD-Signature", protocol.Sign(c.Identity.Private, protocol.CanonicalRequest(method, r.URL.RequestURI(), ts, nonce, pub, body)))
	r.Header.Set("Content-Type", "application/json")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	return r, nil
}
func (c *Client) Do(ctx context.Context, method, path string, input, output any) error {
	return c.DoToken(ctx, method, path, input, output, "")
}
func (c *Client) DoToken(ctx context.Context, method, path string, input, output any, token string) error {
	r, e := c.Request(ctx, method, path, input, token)
	if e != nil {
		return e
	}
	resp, e := c.HTTP.Do(r)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	data, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if e != nil {
		return e
	}
	if resp.StatusCode/100 != 2 {
		var problem struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(data, &problem)
		if problem.Error == "" {
			problem.Error = "coordinator request failed"
		}
		return &HTTPError{resp.StatusCode, problem.Error}
	}
	if output != nil {
		return json.Unmarshal(data, output)
	}
	return nil
}
func (c *Client) Register(ctx context.Context, token string) (protocol.Device, error) {
	var d protocol.Device
	e := c.DoToken(ctx, "POST", "/v1/register", map[string]string{"name": c.Config.Name}, &d, token)
	return d, e
}
func (c *Client) Peer(ctx context.Context, id string) (protocol.Peer, error) {
	var p protocol.Peer
	e := c.Do(ctx, "GET", "/v1/peer?id="+url.QueryEscape(id), nil, &p)
	return p, e
}
func (c *Client) CreateSession(ctx context.Context, target, kind string) (protocol.SessionResponse, error) {
	i := protocol.Intent{From: c.Identity.ID, To: target, Kind: kind, Nonce: protocol.RandomHex(16), IssuedAt: time.Now().Unix()}
	i.Sign(c.Identity.Private)
	var s protocol.SessionResponse
	e := c.Do(ctx, "POST", "/v1/sessions", i, &s)
	return s, e
}
func (c *Client) Status(ctx context.Context, id string) (protocol.SessionStatus, error) {
	var s protocol.SessionStatus
	e := c.Do(ctx, "GET", "/v1/session-status?id="+url.QueryEscape(id), nil, &s)
	return s, e
}

type relayConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *relayConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
func (c *relayConn) CloseWrite() error {
	if w, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return w.CloseWrite()
	}
	return c.Conn.Close()
}
func (c *Client) Relay(ctx context.Context, session, side, ticket string) (net.Conn, error) {
	decodedTicket, ticketErr := hex.DecodeString(ticket)
	if !protocol.ValidNonce(session) || (side != "a" && side != "b") || ticketErr != nil || len(decodedTicket) != 32 {
		return nil, errors.New("invalid relay credentials")
	}
	u, e := url.Parse(c.Config.Server)
	if e != nil {
		return nil, e
	}
	address := u.Host
	if u.Port() == "" {
		address = net.JoinHostPort(u.Hostname(), "443")
	}
	tc := c.TLS.Clone()
	tc.ServerName = u.Hostname()
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: tc}
	raw, e := d.DialContext(ctx, "tcp", address)
	if e != nil {
		return nil, e
	}
	stop := context.AfterFunc(ctx, func() { raw.Close() })
	defer stop()
	_ = raw.SetDeadline(time.Now().Add(8 * time.Second))
	_, e = fmt.Fprintf(raw, "CONNECT /v1/relay/%s/%s HTTP/1.1\r\nHost: %s\r\nAuthorization: Bearer %s\r\n\r\n", session, side, u.Host, ticket)
	if e != nil {
		raw.Close()
		return nil, e
	}
	br := bufio.NewReader(raw)
	resp, e := http.ReadResponse(br, &http.Request{Method: "CONNECT"})
	if e != nil {
		raw.Close()
		return nil, e
	}
	if resp.StatusCode != 200 {
		raw.Close()
		return nil, fmt.Errorf("relay refused: HTTP %d", resp.StatusCode)
	}
	_ = raw.SetDeadline(time.Time{})
	return &relayConn{raw, br}, nil
}
