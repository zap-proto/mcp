// Client — an MCP client speaking ZAP.
//
//	t := zapmcp.Dial("unix", "/run/zip/tools.sock")
//	out, err := t.Call(ctx, "tools/call", args)
//
// Connection management is a free-list pool of idle connections. Do pulls an
// idle conn (or dials one), runs a single exchange, and returns it. Because
// [Unmarshal] copies the frame out, the connection is reusable the instant the
// answer is decoded — there is no body to drain and no close handshake.

package zapmcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

// pooled is one idle connection with its reader and scratch buffers, so a warm
// connection marshals, writes, reads and decodes without allocating.
type pooled struct {
	c    net.Conn
	br   *bufio.Reader
	wbuf []byte
	rbuf []byte
}

// Transport speaks MCP over ZAP to one address. The zero value is invalid; use
// [Dial].
type Transport struct {
	network string
	addr    string
	session string
	seq     atomic.Uint64

	dial time.Duration
	rcv  time.Duration
	idle int

	mu   sync.Mutex
	free []*pooled // LIFO; most-recently-returned is hottest
}

// Dial returns a Transport speaking to addr over network, mirroring net.Dial:
// the network is a value ("tcp", "unix"), not a family of functions. A unix
// address is a socket path and carries the same frames as tcp — the wire does
// not change with the network.
//
//	zapmcp.Dial("unix", "/run/zip/tools.sock")
//	zapmcp.Dial("tcp", "tools.hanzo.svc:9655")
//
// Dialing is lazy; the first call opens the connection.
func Dial(network, addr string) *Transport {
	if network == "" {
		network = "tcp"
	}
	return &Transport{
		network: network, addr: addr,
		dial: 10 * time.Second,
		rcv:  30 * time.Second,
		idle: 32,
	}
}

// Session names the conversation this transport's frames belong to. Set it when
// the peer correlates on it; left empty, frames carry no session.
func (t *Transport) Session(id string) { t.session = id }

// Do runs one exchange and returns the answer. A [Notify] expects none, so it
// is written and nil is returned.
func (t *Transport) Do(f *Frame) (*Frame, error) {
	if t.addr == "" {
		return nil, fmt.Errorf("zapmcp: Transport has no address (use Dial)")
	}
	if f.Session == "" {
		f.Session = t.session
	}
	if f.Seq == 0 {
		f.Seq = t.seq.Add(1)
	}

	p, err := t.acquire()
	if err != nil {
		return nil, fmt.Errorf("zapmcp: dial %s: %w", t.addr, err)
	}

	p.wbuf = append(p.wbuf[:0], 0, 0, 0, 0)
	if p.wbuf, err = Append(p.wbuf, f); err != nil {
		p.c.Close()
		return nil, err
	}
	if err := write(p.c, p.wbuf); err != nil {
		p.c.Close()
		return nil, fmt.Errorf("zapmcp: write %s: %w", f.Method, err)
	}
	if f.Kind == Notify {
		t.release(p)
		return nil, nil
	}

	if t.rcv > 0 {
		_ = p.c.SetReadDeadline(time.Now().Add(t.rcv))
	}
	if p.rbuf, err = read(p.br, p.rbuf); err != nil {
		p.c.Close()
		return nil, fmt.Errorf("zapmcp: read answer to %s: %w", f.Method, err)
	}
	var ans Frame
	if err := Unmarshal(p.rbuf, &ans); err != nil {
		p.c.Close()
		return nil, fmt.Errorf("zapmcp: decode answer to %s: %w", f.Method, err)
	}
	_ = p.c.SetReadDeadline(time.Time{})
	t.release(p)
	return &ans, nil
}

// Call is the typed round trip: invoke method with JSON params and get the JSON
// result. A refusal comes back as the peer's *[Error], so errors.As on this
// side sees the code the far end chose rather than a stringified copy.
//
// ctx is honored to the extent the transport allows: an already-cancelled ctx
// fails before the wire, and the read timeout bounds the call.
func (t *Transport) Call(ctx context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("zapmcp: call %s: %w", method, err)
	}
	n := t.seq.Add(1)
	ans, err := t.Do(&Frame{
		Kind: Request, Method: method, Params: params, Codec: JSON,
		Seq: n, ID: strconv.FormatUint(n, 10),
	})
	if err != nil {
		return nil, err
	}
	if ans.Err != nil {
		return nil, ans.Err
	}
	return ans.Result, nil
}

// Notify sends a message that expects no answer.
func (t *Transport) Notify(method string, params json.RawMessage) error {
	_, err := t.Do(&Frame{Kind: Notify, Method: method, Params: params, Codec: JSON})
	return err
}

func (t *Transport) acquire() (*pooled, error) {
	t.mu.Lock()
	if n := len(t.free); n > 0 {
		p := t.free[n-1]
		t.free = t.free[:n-1]
		t.mu.Unlock()
		return p, nil
	}
	t.mu.Unlock()

	c, err := net.DialTimeout(t.network, t.addr, t.dial)
	if err != nil {
		return nil, err
	}
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetKeepAlive(true)
		_ = tc.SetKeepAlivePeriod(30 * time.Second)
		_ = tc.SetNoDelay(true)
	}
	return &pooled{c: c, br: bufio.NewReaderSize(c, bufSize)}, nil
}

func (t *Transport) release(p *pooled) {
	t.mu.Lock()
	if len(t.free) >= t.idle {
		t.mu.Unlock()
		_ = p.c.Close()
		return
	}
	t.free = append(t.free, p)
	t.mu.Unlock()
}

// CloseIdleConnections closes every pooled connection. Calls in flight are
// unaffected, and the Transport stays usable — a later call redials.
func (t *Transport) CloseIdleConnections() {
	t.mu.Lock()
	free := t.free
	t.free = nil
	t.mu.Unlock()
	for _, p := range free {
		_ = p.c.Close()
	}
}
