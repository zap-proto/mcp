// Server — a [Handler] served over ZAP.
//
// An MCP server hands its handler straight in, and that is the whole of what
// running MCP over ZAP takes:
//
//	srv := &zapmcp.Server{Network: "unix", Addr: "/run/zip/tools.sock", Handler: h}
//	srv.ListenAndServe()
//
// There is no HTTP anywhere in the path — no listener, no request, no status
// code. The server reads frames off each accepted connection, hands each to the
// handler, and writes the answer back. Connections are kept alive across
// frames.

package zapmcp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"time"
)

// Handler answers one frame. Returning nil answers nothing, which is what a
// [Notify] wants and what a handler that has already streamed its reply says.
//
// The context is the connection's, carrying the [Peer] the frame arrived from,
// so a handler reads who is calling from the same place a zip op does and never
// from the frame's own fields, which the caller wrote.
type Handler func(ctx context.Context, f *Frame) *Frame

// Server serves a Handler over ZAP. The zero value is usable once Addr and
// Handler are set; the knobs mirror zaphttp.Server.
type Server struct {
	Addr string // ":9655" if empty
	// Network mirrors net.Listen's first argument: "tcp" when empty, or "unix"
	// to serve the same frames on a socket path given in Addr. The wire is
	// identical either way; only the plumbing differs.
	Network      string
	Handler      Handler // required
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration

	mu       sync.Mutex
	listener net.Listener
	closed   bool
}

// ListenAndServe is the convenience equivalent for a one-line main.
func ListenAndServe(addr string, h Handler) error {
	return (&Server{Addr: addr, Handler: h}).ListenAndServe()
}

// ListenAndServe binds Addr and serves until Close.
func (s *Server) ListenAndServe() error {
	addr := s.Addr
	if addr == "" {
		addr = ":9655"
	}
	network := s.Network
	if network == "" {
		network = "tcp"
	}
	if network == "unix" {
		// A unix socket outlives the process that made it, so a crashed
		// predecessor leaves a file that bind refuses. Clearing a stale socket is
		// what makes restart-in-place work; a LIVE one still fails, because
		// something is genuinely listening.
		if c, err := net.DialTimeout("unix", addr, 200*time.Millisecond); err == nil {
			_ = c.Close()
			return fmt.Errorf("zapmcp: %s is already served by a live process", addr)
		}
		_ = os.Remove(addr)
	}
	ln, err := net.Listen(network, addr)
	if err != nil {
		return err
	}
	return s.Serve(ln)
}

// Serve accepts connections on ln, each in its own goroutine, and returns when
// the listener closes.
func (s *Server) Serve(ln net.Listener) error {
	if s.Handler == nil {
		return errors.New("zapmcp: Server.Handler is nil")
	}
	s.mu.Lock()
	s.listener = ln
	s.mu.Unlock()

	for {
		conn, err := ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return nil
			}
			var nerr net.Error
			if errors.As(err, &nerr) && nerr.Timeout() {
				continue
			}
			return err
		}
		go s.conn(conn)
	}
}

// Bound is the address the server is listening on, which is how a caller that
// asked for port 0 finds out what it got. Nil before Serve.
func (s *Server) Bound() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener == nil {
		return nil
	}
	return s.listener.Addr()
}

// conn serves one connection until it closes or misbehaves.
func (s *Server) conn(c net.Conn) {
	defer c.Close()
	br := bufio.NewReaderSize(c, bufSize)
	ctx := with(context.Background(), c)

	// Per-connection scratch: the inbound frame is decoded into a Frame (which
	// copies what it keeps) and the answer is built here. Both grow once.
	var in, out []byte
	var f Frame

	for {
		if s.IdleTimeout > 0 {
			_ = c.SetReadDeadline(time.Now().Add(s.IdleTimeout))
		}
		var err error
		in, err = read(br, in)
		if err != nil {
			if !errors.Is(err, io.EOF) && !closedConn(err) {
				log.Printf("zapmcp: read frame: %v", err)
			}
			return
		}
		if s.ReadTimeout > 0 {
			_ = c.SetReadDeadline(time.Now().Add(s.ReadTimeout))
		}
		if err := Unmarshal(in, &f); err != nil {
			log.Printf("zapmcp: malformed frame: %v", err)
			return
		}

		// A handler panic answers -32603 rather than dropping the connection
		// silently: the agent on the other end sees a refusal it can read.
		ans := s.answer(ctx, &f)
		if ans == nil {
			continue // a notification, or a handler that answered elsewhere
		}
		if s.WriteTimeout > 0 {
			_ = c.SetWriteDeadline(time.Now().Add(s.WriteTimeout))
		}
		out = append(out[:0], 0, 0, 0, 0)
		if out, err = Append(out, ans); err != nil {
			log.Printf("zapmcp: marshal answer: %v", err)
			return
		}
		if err := write(c, out); err != nil {
			if !closedConn(err) {
				log.Printf("zapmcp: write frame: %v", err)
			}
			return
		}
	}
}

// answer runs the handler under a recover, and correlates what comes back.
// A Response is returned as the handler built it; anything else is stamped with
// this frame's session, sequence and id, so a handler that answered with a bare
// result still produces a correlatable reply.
func (s *Server) answer(ctx context.Context, f *Frame) (ans *Frame) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("zapmcp: handler panic on %s: %v", f.Method, r)
			ans = f.Fail(CodeInternal, "internal error")
		}
	}()
	ans = s.Handler(ctx, f)
	if ans == nil || f.Kind == Notify {
		return nil
	}
	ans.Kind = Response
	ans.Session, ans.Seq, ans.ID = f.Session, f.Seq, f.ID
	return ans
}

// Close stops the listener; in-flight handlers are not interrupted.
func (s *Server) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.listener == nil {
		return nil
	}
	return s.listener.Close()
}

func closedConn(err error) bool {
	return err != nil && (errors.Is(err, net.ErrClosed) || errors.Is(err, io.EOF))
}
