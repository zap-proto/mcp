package zapmcp

import (
	"context"
	"net"
)

// connKey carries the connection a frame arrived on into the handler's context.
type connKey struct{}

// with binds a connection to the context its frames are answered under.
func with(ctx context.Context, c net.Conn) context.Context {
	return context.WithValue(ctx, connKey{}, c)
}

// Conn is the connection this frame arrived on, or nil when there is none —
// a frame handed to a handler in-process, or a test.
//
// It is the connection itself rather than a digest of it because what a caller
// wants to know differs by deployment and none of it belongs here: the remote
// address, the kernel's peer credential over a unix socket (which is the one
// thing about a caller the caller does not get to state), the transport's key
// material. A codec that pre-chewed those into a struct would have to guess
// which ones matter and would be wrong somewhere.
//
//	if c := zapmcp.Conn(ctx); c != nil {
//	    cred, err := unix.GetsockoptUcred(...)   // whatever this deployment needs
//	}
func Conn(ctx context.Context) net.Conn {
	c, _ := ctx.Value(connKey{}).(net.Conn)
	return c
}
