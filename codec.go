// The ZAP-MCP frame codec.
//
// A frame is one github.com/zap-proto/go message: a 16-byte header (magic
// "ZAP\x00", version, flags, rootOffset=16, size) followed by the root object.
// The root object is a fixed section (fully zero-filled) whose text/bytes
// fields are 8-byte {relOffset,length} slots; every non-empty field's bytes are
// appended to the variable tail in field-declaration order and its slot's
// relOffset is patched to point at them. This is exactly what zap.Builder
// emits, and TestCodec_MatchesBuilder proves it byte for byte.
//
// # One object, three kinds
//
// schema/zap_mcp.zap declares Frame with a Payload union of Request, Response
// and Notify. The union's TAG rides the message header flags (kind<<8), the way
// zaphttp puts its frame type there, so a reader dispatches on the envelope
// without decoding the body. The union's ARMS share one object shape: they
// overlap almost entirely, and a field an arm does not use is a zeroed slot
// with no tail entry — no bytes, no branch, no second offset table to keep in
// step with the first.
//
// Callers append into a reusable buffer ([Append]), so a warm connection
// marshals with no heap allocation.

package zapmcp

import (
	"encoding/binary"
	"fmt"

	zap "github.com/zap-proto/go"
)

// Frame object field offsets within the fixed section. Scalars sit at their
// natural alignment first; every text/bytes slot is 8 bytes.
const (
	fSeq     = 0  // uint64 (8)
	fCodec   = 8  // uint8  (1)
	fCode    = 12 // int32  (4)  -- Err.Code
	fSession = 16 // text   (8)
	fSubject = 24 // text   (8)
	fSig     = 32 // bytes  (8)
	fID      = 40 // text   (8)
	fMethod  = 48 // text   (8)
	fParams  = 56 // bytes  (8)
	fResult  = 64 // bytes  (8)
	fMessage = 72 // text   (8)  -- Err.Message
	fData    = 80 // bytes  (8)  -- Err.Data
	fSize    = 88
)

// fail marks a Response as carrying an error even when the error's own fields
// are all zero. Without it a `&Error{}` — code 0, no message — would decode
// back as a success, and "the call failed and said nothing" would become "the
// call succeeded and returned nothing", which is the opposite answer.
const fail uint16 = 1

// zeroFixed lays down an object's fixed section. Read-only, shared.
var zeroFixed [fSize]byte

// Marshal renders a frame as ZAP wire bytes.
func Marshal(f *Frame) ([]byte, error) { return Append(nil, f) }

// Append appends a frame to dst and returns the extended slice. Passing a
// reused (len-0) buffer makes the steady-state marshal allocation-free;
// [Marshal] is the dst==nil convenience.
func Append(dst []byte, f *Frame) ([]byte, error) {
	if f == nil || f.Kind == 0 {
		return dst, ErrKind
	}
	flags := uint16(f.Kind) << 8
	if f.Kind == Response && f.Err != nil {
		flags |= fail
	}

	start := len(dst)
	dst = append(dst, 'Z', 'A', 'P', 0)
	dst = binary.LittleEndian.AppendUint16(dst, uint16(zap.Version))
	dst = binary.LittleEndian.AppendUint16(dst, flags)
	dst = binary.LittleEndian.AppendUint32(dst, uint32(zap.HeaderSize)) // rootOffset
	dst = binary.LittleEndian.AppendUint32(dst, 0)                      // size, patched below

	obj := len(dst)
	dst = append(dst, zeroFixed[:]...)

	binary.LittleEndian.PutUint64(dst[obj+fSeq:], f.Seq)
	dst[obj+fCodec] = byte(f.Codec)

	// Declaration order, which is the order the tail is written in.
	dst = text(dst, obj+fSession, f.Session)
	dst = text(dst, obj+fSubject, f.Subject)
	dst = bytesAt(dst, obj+fSig, f.Sig)
	dst = text(dst, obj+fID, f.ID)
	dst = text(dst, obj+fMethod, f.Method)
	dst = bytesAt(dst, obj+fParams, f.Params)
	dst = bytesAt(dst, obj+fResult, f.Result)
	if f.Err != nil {
		binary.LittleEndian.PutUint32(dst[obj+fCode:], uint32(f.Err.Code))
		dst = text(dst, obj+fMessage, f.Err.Message)
		dst = bytesAt(dst, obj+fData, f.Err.Data)
	}

	binary.LittleEndian.PutUint32(dst[start+12:], uint32(len(dst)-start))
	return dst, nil
}

// Unmarshal reads a frame's bytes into f. The frame's text and bytes fields
// are COPIED out, so f outlives the buffer it was read from — a server reuses
// its read buffer across requests, and a handler that held a subslice of it
// would see the next request's bytes appear inside the one it is answering.
func Unmarshal(b []byte, f *Frame) error {
	if len(b) < zap.HeaderSize {
		return zap.ErrBufferTooSmall
	}
	if string(b[0:4]) != zap.Magic {
		return zap.ErrInvalidMagic
	}
	if v := binary.LittleEndian.Uint16(b[4:6]); v != 1 && v != 2 {
		return zap.ErrInvalidVersion
	}
	size := int(binary.LittleEndian.Uint32(b[12:16]))
	if size < zap.HeaderSize || size > len(b) {
		return zap.ErrBufferTooSmall
	}
	flags := binary.LittleEndian.Uint16(b[6:8])
	kind := Kind(flags >> 8)
	if kind != Request && kind != Response && kind != Notify {
		return fmt.Errorf("%w: %d", ErrKind, flags>>8)
	}
	obj := int(binary.LittleEndian.Uint32(b[8:12]))
	if obj < zap.HeaderSize || obj+fSize > size {
		return zap.ErrInvalidOffset
	}
	msg := b[:size]

	*f = Frame{
		Kind:  kind,
		Seq:   binary.LittleEndian.Uint64(msg[obj+fSeq:]),
		Codec: Codec(msg[obj+fCodec]),
	}
	var err error
	if f.Session, err = readText(msg, obj+fSession); err != nil {
		return err
	}
	if f.Subject, err = readText(msg, obj+fSubject); err != nil {
		return err
	}
	if f.Sig, err = readBytes(msg, obj+fSig); err != nil {
		return err
	}
	if f.ID, err = readText(msg, obj+fID); err != nil {
		return err
	}
	if f.Method, err = readText(msg, obj+fMethod); err != nil {
		return err
	}
	if f.Params, err = readBytes(msg, obj+fParams); err != nil {
		return err
	}
	if f.Result, err = readBytes(msg, obj+fResult); err != nil {
		return err
	}
	if flags&fail != 0 {
		e := &Error{Code: int32(binary.LittleEndian.Uint32(msg[obj+fCode:]))}
		if e.Message, err = readText(msg, obj+fMessage); err != nil {
			return err
		}
		if e.Data, err = readBytes(msg, obj+fData); err != nil {
			return err
		}
		f.Err, f.Result = e, nil
	}
	return nil
}

// ---- slot plumbing ----

// text appends a string to the tail and patches its slot; an empty string
// leaves the slot the zeroed null it started as.
func text(dst []byte, slot int, s string) []byte {
	if s == "" {
		return dst
	}
	at := len(dst)
	dst = append(dst, s...)
	return patch(dst, slot, at)
}

// bytesAt is [text] for a byte slice.
func bytesAt(dst []byte, slot int, b []byte) []byte {
	if len(b) == 0 {
		return dst
	}
	at := len(dst)
	dst = append(dst, b...)
	return patch(dst, slot, at)
}

// patch points an 8-byte {relOffset,length} slot at tail data already appended
// starting at from. The offset is RELATIVE to the slot, which is what makes an
// object relocatable.
func patch(dst []byte, slot, from int) []byte {
	n := len(dst) - from
	if n == 0 {
		return dst
	}
	binary.LittleEndian.PutUint32(dst[slot:], uint32(from-slot))
	binary.LittleEndian.PutUint32(dst[slot+4:], uint32(n))
	return dst
}

// slot reads a {relOffset,length} pair, bounds-checked against the message.
func slot(msg []byte, at int) (start, n int, err error) {
	rel := binary.LittleEndian.Uint32(msg[at:])
	n = int(binary.LittleEndian.Uint32(msg[at+4:]))
	if rel == 0 || n == 0 {
		return 0, 0, nil
	}
	start = at + int(rel)
	if start < 0 || start+n > len(msg) {
		return 0, 0, zap.ErrOutOfBounds
	}
	return start, n, nil
}

func readText(msg []byte, at int) (string, error) {
	start, n, err := slot(msg, at)
	if err != nil || n == 0 {
		return "", err
	}
	return string(msg[start : start+n]), nil
}

func readBytes(msg []byte, at int) ([]byte, error) {
	start, n, err := slot(msg, at)
	if err != nil || n == 0 {
		return nil, err
	}
	return append([]byte(nil), msg[start:start+n]...), nil
}
