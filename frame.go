// Package zapmcp implements Model Context Protocol semantics over the ZAP
// transport, the way [zaphttp] implements HTTP semantics over it: ZAP is the
// wire, MCP is one of the protocols spoken on it, and neither knows about the
// other.
//
// # One value, two projections
//
// MCP is JSON-RPC 2.0, and an agent reaches a tool server today over stdio or
// HTTP+SSE. Both of those carry JSON text; ZAP carries binary frames. Rather
// than model the difference — a JSON message type and a ZAP message type, kept
// in step by hand — there is ONE value, [Frame], and two renderings of it:
//
//	zapmcp.Marshal(f)      the ZAP wire (schema/zap_mcp.zap)
//	json.Marshal(f)        the JSON-RPC 2.0 message
//
// A server therefore answers frames and does not know which door the frame
// came in. That is what lets an MCP server run with no HTTP listener at all:
//
//	srv := &zapmcp.Server{Addr: "/run/zip/tools.sock", Network: "unix", Handler: h}
//	srv.ListenAndServe()
//
// # Provenance
//
// Every frame carries who signed it (Subject, Sig) and where it sits in a
// session (Session, Seq). Those fields ride the frame rather than a header or
// an operator log, so a verifier years later can reconstruct whether a given
// agent issued a given call without trusting either. This package carries them;
// it does not mint or check signatures — that is the transport's key material
// and the caller's policy, and neither belongs in a message codec.
//
// [zaphttp]: https://github.com/zap-proto/http
package zapmcp

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Kind is which of the three MCP messages a frame is. It is the schema's
// Payload union, encoded in the ZAP header flags exactly as zaphttp encodes its
// frame type — the discriminator rides the envelope, so a reader dispatches
// without decoding the body.
type Kind uint8

const (
	// Request expects exactly one Response, correlated by ID.
	Request Kind = 1
	// Response answers one Request; Result or Err is set, never both.
	Response Kind = 2
	// Notify expects no answer and carries no ID.
	Notify Kind = 3
)

// String renders a kind for a log line or an error.
func (k Kind) String() string {
	switch k {
	case Request:
		return "request"
	case Response:
		return "response"
	case Notify:
		return "notify"
	}
	return fmt.Sprintf("kind(%d)", uint8(k))
}

// Codec is how a frame's Params or Result is encoded — the schema's ParamKind,
// with its values in the schema's order.
//
// The default is JSON, because MCP's own payloads (tool schemas, tool results)
// are defined in JSON and an agent reads them as JSON whichever wire carried
// them. Binary is for a peer that has the type on both ends and would rather
// not serialize through text to reach itself.
type Codec uint8

const (
	// JSON is a UTF-8 JSON document. The zero value, so a frame built by hand
	// carries what MCP defines.
	JSON Codec = 0
	// Binary is a ZAP-encoded value; both peers hold the type.
	Binary Codec = 1
	// None says the field is absent — distinct from a JSON `null`, which is a
	// value.
	None Codec = 2
)

// String renders a codec for a log line or an error.
func (c Codec) String() string {
	switch c {
	case JSON:
		return "json"
	case Binary:
		return "binary"
	case None:
		return "none"
	}
	return fmt.Sprintf("codec(%d)", uint8(c))
}

// Error is a JSON-RPC 2.0 error object: the failure a Response carries instead
// of a Result.
type Error struct {
	Code    int32
	Message string
	Data    []byte // raw, encoded per the frame's Codec; nil when absent
}

// Error renders the failure, so an *Error is an ordinary Go error.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("mcp error %d: %s", e.Code, e.Message)
}

// The JSON-RPC 2.0 error codes, named once so a server and its clients spell
// them the same way.
const (
	CodeParse    int32 = -32700
	CodeRequest  int32 = -32600
	CodeMethod   int32 = -32601
	CodeParams   int32 = -32602
	CodeInternal int32 = -32603
)

// Frame is one MCP message, as a value: which message it is, what it says, and
// what the session knows about who is saying it. It is the unit on the wire
// (schema/zap_mcp.zap) and the unit a [Handler] answers.
//
// One struct covers all three kinds rather than three types with a union in
// front of them, because the three overlap almost entirely — every one of them
// carries the envelope, two of them carry a method and params, two carry an id
// — and Kind already says which fields mean anything. A field that does not
// apply is a zero, and a zero costs nothing on the wire: an empty slot is not
// written.
type Frame struct {
	// Kind is which message this is. A zero Kind is not a frame.
	Kind Kind

	// Session and Seq place this frame in a conversation: which one, and where.
	// A server correlates on them; [Server] copies them onto the answer so a
	// handler does not have to remember to.
	Session string
	Seq     uint64

	// Subject names whose key signed this frame, and Sig is that signature.
	// Both are carried, neither is minted or verified here.
	Subject string
	Sig     []byte

	// ID correlates a Request with its Response, VERBATIM as JSON-RPC spells
	// it — `1`, `"abc"` — quotes and all. Storing the token rather than the
	// value is what makes the two projections lossless: a client that sent the
	// string "1" gets back the string "1" and not the number 1, which a rule
	// that re-derived the quoting could not promise. Empty on a Notify.
	ID string

	// Method is what a Request or a Notify invokes ("tools/call"). Empty on a
	// Response, which is identified by its ID.
	Method string

	// Params are a Request's or a Notify's arguments; Result is a Response's
	// answer. Both are raw, encoded per Codec — this package moves them and
	// does not read them.
	Params []byte
	Result []byte

	// Err is a Response's failure. Exactly one of Result and Err is set.
	Err *Error

	// Codec is how Params, Result and Err.Data are encoded. JSON is the zero
	// value and what MCP defines.
	Codec Codec
}

// Answer builds the Response to this Request, carrying its session, sequence
// and id across so a handler states only the result.
//
// It is the one place a reply is correlated, which is why a handler cannot get
// it wrong: there is no second spelling in which the id is copied by hand.
func (f *Frame) Answer(result []byte) *Frame {
	return &Frame{
		Kind: Response, Session: f.Session, Seq: f.Seq, ID: f.ID,
		Result: result, Codec: f.Codec,
	}
}

// Fail builds the Response that refuses this Request. See [Frame.Answer].
func (f *Frame) Fail(code int32, msg string) *Frame {
	return &Frame{
		Kind: Response, Session: f.Session, Seq: f.Seq, ID: f.ID,
		Err: &Error{Code: code, Message: msg}, Codec: f.Codec,
	}
}

// ---- the JSON-RPC 2.0 projection ----

// jsonrpc is the version every message carries, per the spec.
const jsonrpc = "2.0"

// wire is the JSON-RPC 2.0 shape, used for both directions. It exists so the
// field order and the omitempty rules are written once.
type wire struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *wireError      `json:"error,omitempty"`
}

type wireError struct {
	Code    int32           `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// MarshalJSON renders the frame as a JSON-RPC 2.0 message — the projection an
// agent speaking HTTP or stdio reads. It is a method rather than a package
// function so json.Marshal of anything containing a Frame is correct too.
//
// Two members are written even when they are empty, because JSON-RPC reads
// their ABSENCE as a different message rather than as a missing detail:
//
//   - a Response carries `"id":null` when it has no id, which is what the spec
//     requires of an answer to a request whose id could not be read (a parse
//     error, an invalid request);
//   - a successful Response carries `"result":null` when it returned nothing,
//     because omitting the member makes the message a malformed response rather
//     than a void one, and strict clients reject it.
func (f Frame) MarshalJSON() ([]byte, error) {
	w := wire{JSONRPC: jsonrpc, Method: f.Method}
	switch {
	case f.ID != "":
		w.ID = json.RawMessage(f.ID)
	case f.Kind == Response:
		w.ID = json.RawMessage("null")
	}
	switch f.Kind {
	case Request, Notify:
		w.Params = raw(f.Params, f.Codec)
	case Response:
		if f.Err != nil {
			w.Error = &wireError{Code: f.Err.Code, Message: f.Err.Message, Data: raw(f.Err.Data, f.Codec)}
			break
		}
		if w.Result = raw(f.Result, f.Codec); w.Result == nil {
			w.Result = json.RawMessage("null")
		}
	default:
		return nil, fmt.Errorf("zapmcp: cannot render %s as JSON-RPC", f.Kind)
	}
	return json.Marshal(w)
}

// raw is a payload as JSON, or nil when there is none to render. A Binary
// payload has no JSON rendering — it is bytes both peers hold the type for —
// so it is omitted rather than mangled into a string.
func raw(b []byte, c Codec) json.RawMessage {
	if len(b) == 0 || c != JSON {
		return nil
	}
	return json.RawMessage(b)
}

// UnmarshalJSON reads a JSON-RPC 2.0 message into the frame, deciding the Kind
// the way the spec does: a message with a method is a Request unless it carries
// no id, in which case it is a Notify; anything else is a Response.
func (f *Frame) UnmarshalJSON(b []byte) error {
	var w wire
	if err := json.Unmarshal(b, &w); err != nil {
		return err
	}
	*f = Frame{ID: idOf(w.ID), Method: w.Method, Codec: JSON}
	switch {
	case w.Method != "" && f.ID == "":
		f.Kind, f.Params = Notify, w.Params
	case w.Method != "":
		f.Kind, f.Params = Request, w.Params
	default:
		f.Kind, f.Result = Response, w.Result
		if w.Error != nil {
			f.Result, f.Err = nil, &Error{Code: w.Error.Code, Message: w.Error.Message, Data: w.Error.Data}
		}
	}
	return nil
}

// idOf is the id token, verbatim, with an explicit JSON null read as absent —
// a null id is JSON-RPC's way of saying "no id", which is the same fact an
// omitted member states.
func idOf(id json.RawMessage) string {
	if len(id) == 0 || string(id) == "null" {
		return ""
	}
	return string(id)
}

// ErrKind is returned when a frame is not one this codec can carry — a zero
// Kind, or a value from a future revision.
var ErrKind = errors.New("zapmcp: unknown frame kind")
