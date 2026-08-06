package zapmcp

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// serve brings up a server on a unix socket in the test's own temp dir and
// returns a transport dialled at it. There is no HTTP listener anywhere in
// this file — that is the property under test.
func serve(t *testing.T, h Handler) *Transport {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "mcp.sock")
	srv := &Server{Network: "unix", Addr: sock, Handler: h}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	done := make(chan struct{})
	go func() { defer close(done); _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
		<-done
	})

	c := Dial("unix", sock)
	t.Cleanup(c.CloseIdleConnections)
	return c
}

func TestServe_Call(t *testing.T) {
	c := serve(t, func(ctx context.Context, f *Frame) *Frame {
		if f.Method != "tools/call" {
			return f.Fail(CodeMethod, "method not found: "+f.Method)
		}
		return f.Answer([]byte(`{"content":[{"type":"text","text":"42"}]}`))
	})

	out, err := c.Call(context.Background(), "tools/call", json.RawMessage(`{"name":"answer"}`))
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if string(out) != `{"content":[{"type":"text","text":"42"}]}` {
		t.Fatalf("result = %s", out)
	}
}

// A refusal arrives as the peer's own *Error, so errors.As sees the code the
// far end chose rather than a stringified copy of it.
func TestServe_Refusal(t *testing.T) {
	c := serve(t, func(ctx context.Context, f *Frame) *Frame {
		return f.Fail(CodeMethod, "unknown tool: "+f.Method)
	})

	_, err := c.Call(context.Background(), "nope", nil)
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("Call error = %T %v, want *zapmcp.Error", err, err)
	}
	if e.Code != CodeMethod || e.Message != "unknown tool: nope" {
		t.Fatalf("error = %+v", e)
	}
}

// A panicking handler answers -32603. Dropping the connection instead would
// leave the agent with a transport failure it cannot interpret.
func TestServe_PanicAnswers(t *testing.T) {
	c := serve(t, func(ctx context.Context, f *Frame) *Frame { panic("boom") })

	_, err := c.Call(context.Background(), "x", nil)
	var e *Error
	if !errors.As(err, &e) || e.Code != CodeInternal {
		t.Fatalf("panic answered %v, want a -32603 error", err)
	}
}

// A notification expects no answer, and the connection stays usable after one —
// a server that wrote a reply anyway would desynchronise every later call on
// that connection.
func TestServe_NotifyThenCall(t *testing.T) {
	var got struct {
		sync.Mutex
		method string
	}
	c := serve(t, func(ctx context.Context, f *Frame) *Frame {
		if f.Kind == Notify {
			got.Lock()
			got.method = f.Method
			got.Unlock()
			return f.Answer([]byte(`"ignored"`)) // even so, nothing may go out
		}
		return f.Answer([]byte(`"ok"`))
	})

	if err := c.Notify("notifications/initialized", json.RawMessage(`{}`)); err != nil {
		t.Fatalf("Notify: %v", err)
	}
	out, err := c.Call(context.Background(), "ping", nil)
	if err != nil {
		t.Fatalf("Call after Notify: %v", err)
	}
	if string(out) != `"ok"` {
		t.Fatalf("the call read the notification's reply: %s", out)
	}
	got.Lock()
	defer got.Unlock()
	if got.method != "notifications/initialized" {
		t.Fatalf("notification not delivered: %q", got.method)
	}
}

// The connection is pooled, so a second call costs a round trip and no dial.
// A server that answered only the first frame on a connection would fail here
// and pass every single-call test.
func TestServe_KeepsTheConnection(t *testing.T) {
	c := serve(t, func(ctx context.Context, f *Frame) *Frame { return f.Answer(f.Params) })

	for i := 0; i < 8; i++ {
		out, err := c.Call(context.Background(), "echo", json.RawMessage(`{"i":`+string(rune('0'+i))+`}`))
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
		if want := `{"i":` + string(rune('0'+i)) + `}`; string(out) != want {
			t.Fatalf("call %d = %s, want %s", i, out, want)
		}
	}
}

// The handler is told which connection the frame arrived on, which is how a
// deployment reads the kernel's peer credential over a unix socket — the one
// thing about a caller the caller does not get to state.
func TestServe_HandlerSeesTheConnection(t *testing.T) {
	seen := make(chan string, 1)
	c := serve(t, func(ctx context.Context, f *Frame) *Frame {
		if conn := Conn(ctx); conn != nil {
			seen <- conn.LocalAddr().Network()
		} else {
			seen <- ""
		}
		return f.Answer(nil)
	})
	if _, err := c.Call(context.Background(), "x", nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	select {
	case network := <-seen:
		if network != "unix" {
			t.Fatalf("Conn(ctx) reported %q, want unix", network)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("handler never ran")
	}
}

// Concurrent callers each take their own connection from the pool.
func TestServe_Concurrent(t *testing.T) {
	c := serve(t, func(ctx context.Context, f *Frame) *Frame { return f.Answer(f.Params) })

	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := c.Call(context.Background(), "echo", json.RawMessage(`"x"`))
			if err != nil {
				errs <- err
				return
			}
			if string(out) != `"x"` {
				errs <- errors.New("wrong echo: " + string(out))
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

// An already-cancelled context fails before the wire.
func TestServe_CancelledContext(t *testing.T) {
	c := serve(t, func(ctx context.Context, f *Frame) *Frame { return f.Answer(nil) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Call(ctx, "x", nil); err == nil {
		t.Fatal("a cancelled context reached the wire")
	}
}

// A Close that beats the accept loop to the lock must still stop the server.
// Without the flag check in Serve, this hangs forever: Close finds no listener
// and closes nothing, then Serve installs one and blocks in Accept on it. That
// is every short-lived server and every test that shuts down promptly.
func TestServe_CloseBeforeServe(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "race.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &Server{Handler: func(context.Context, *Frame) *Frame { return nil }}
	if err := srv.Close(); err != nil { // before Serve ever runs
		t.Fatalf("Close: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Serve after Close = %v, want a clean return", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve blocked in Accept on a listener Close had already given up on")
	}
}
