package zapmcp

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"testing"

	zap "github.com/zap-proto/go"
)

// full is a frame with every field set, so a round trip that drops one is
// visible rather than merely untested.
func full() *Frame {
	return &Frame{
		Kind:    Request,
		Session: "s-1",
		Seq:     7,
		Subject: "agent:did:zap:abc",
		Sig:     []byte{0xde, 0xad, 0xbe, 0xef},
		ID:      `"call-1"`,
		Method:  "tools/call",
		Params:  []byte(`{"name":"flags_bool"}`),
		Codec:   JSON,
	}
}

// The encoder writes the SAME bytes the reference zap.Builder path writes.
// Without this the hand-rolled offset discipline in codec.go is only checked
// against itself, and a decoder that agreed with a wrong encoder would pass
// every round-trip test in this file while being unreadable by every other
// language's ZAP runtime.
func TestCodec_MatchesBuilder(t *testing.T) {
	f := full()

	got, err := Marshal(f)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	b := zap.NewBuilder(256)
	ob := b.StartObject(fSize)
	ob.SetUint64(fSeq, f.Seq)
	ob.SetUint8(fCodec, uint8(f.Codec))
	// Declaration order — the order the tail is written in.
	ob.SetText(fSession, f.Session)
	ob.SetText(fSubject, f.Subject)
	ob.SetBytes(fSig, f.Sig)
	ob.SetText(fID, f.ID)
	ob.SetText(fMethod, f.Method)
	ob.SetBytes(fParams, f.Params)
	ob.FinishAsRoot()
	want := b.FinishWithFlags(uint16(f.Kind) << 8)

	if !bytes.Equal(got, want) {
		t.Fatalf("encoder disagrees with zap.Builder\n got %d bytes: %x\nwant %d bytes: %x",
			len(got), got, len(want), want)
	}
}

func TestCodec_RoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    *Frame
	}{
		{"request", full()},
		{"notify", &Frame{Kind: Notify, Method: "notifications/initialized", Params: []byte(`{}`)}},
		{"response", &Frame{Kind: Response, ID: "1", Result: []byte(`{"ok":true}`)}},
		{"failure", &Frame{Kind: Response, ID: "1", Err: &Error{Code: CodeMethod, Message: "no such tool", Data: []byte(`"x"`)}}},
		{"binary", &Frame{Kind: Request, Method: "m", Params: []byte{0, 1, 2}, Codec: Binary}},
		{"bare", &Frame{Kind: Request, Method: "ping"}},
		// A failure whose fields are ALL zero still has to decode as a failure.
		{"empty failure", &Frame{Kind: Response, ID: "1", Err: &Error{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := Marshal(tc.f)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			var got Frame
			if err := Unmarshal(b, &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got.Kind != tc.f.Kind || got.Method != tc.f.Method || got.ID != tc.f.ID ||
				got.Session != tc.f.Session || got.Seq != tc.f.Seq || got.Subject != tc.f.Subject ||
				got.Codec != tc.f.Codec {
				t.Fatalf("envelope\n got %+v\nwant %+v", got, *tc.f)
			}
			if !bytes.Equal(got.Params, tc.f.Params) || !bytes.Equal(got.Result, tc.f.Result) ||
				!bytes.Equal(got.Sig, tc.f.Sig) {
				t.Fatalf("payload\n got %+v\nwant %+v", got, *tc.f)
			}
			switch {
			case (got.Err == nil) != (tc.f.Err == nil):
				t.Fatalf("Err presence: got %v, want %v", got.Err, tc.f.Err)
			case got.Err != nil:
				if got.Err.Code != tc.f.Err.Code || got.Err.Message != tc.f.Err.Message ||
					!bytes.Equal(got.Err.Data, tc.f.Err.Data) {
					t.Fatalf("Err\n got %+v\nwant %+v", got.Err, tc.f.Err)
				}
			}
		})
	}
}

// The server reuses one read buffer across every frame on a connection, so a
// decoder that handed back subslices of it would let the NEXT request's bytes
// appear inside the frame a handler is still answering. Unmarshal copies; this
// is what proves it.
func TestCodec_CopiesOutOfTheBuffer(t *testing.T) {
	b, err := Marshal(full())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	buf := append([]byte(nil), b...)

	var f Frame
	if err := Unmarshal(buf, &f); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	method, params := f.Method, append([]byte(nil), f.Params...)

	for i := range buf { // the next frame lands in the same buffer
		buf[i] = 0xff
	}
	if f.Method != method {
		t.Errorf("Method aliases the read buffer: %q became %q", method, f.Method)
	}
	if !bytes.Equal(f.Params, params) {
		t.Errorf("Params alias the read buffer: %q became %q", params, f.Params)
	}
}

func TestCodec_Refuses(t *testing.T) {
	good, _ := Marshal(full())

	bad := func(mut func([]byte) []byte) []byte { return mut(append([]byte(nil), good...)) }

	for _, tc := range []struct {
		name string
		b    []byte
	}{
		{"short", good[:8]},
		{"magic", bad(func(b []byte) []byte { b[0] = 'X'; return b })},
		{"version", bad(func(b []byte) []byte { binary.LittleEndian.PutUint16(b[4:6], 99); return b })},
		{"kind", bad(func(b []byte) []byte { binary.LittleEndian.PutUint16(b[6:8], 0x0900); return b })},
		{"size", bad(func(b []byte) []byte { binary.LittleEndian.PutUint32(b[12:16], 1<<30); return b })},
		{"root", bad(func(b []byte) []byte { binary.LittleEndian.PutUint32(b[8:12], 4); return b })},
		{"slot points out of the frame", bad(func(b []byte) []byte {
			binary.LittleEndian.PutUint32(b[zap.HeaderSize+fMethod+4:], 1<<20)
			return b
		})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var f Frame
			if err := Unmarshal(tc.b, &f); err == nil {
				t.Fatalf("accepted a %s frame: %+v", tc.name, f)
			}
		})
	}
	if _, err := Marshal(&Frame{}); !errors.Is(err, ErrKind) {
		t.Errorf("Marshal of a kindless frame = %v, want ErrKind", err)
	}
}

// The id is carried VERBATIM, so a client that correlated on the number 1 does
// not get back the string "1". A rule that re-derived the quoting could not
// promise this, which is why the token is what travels.
func TestJSON_IDIsVerbatim(t *testing.T) {
	for _, in := range []string{`{"jsonrpc":"2.0","id":1,"method":"ping"}`, `{"jsonrpc":"2.0","id":"1","method":"ping"}`} {
		var f Frame
		if err := json.Unmarshal([]byte(in), &f); err != nil {
			t.Fatalf("UnmarshalJSON(%s): %v", in, err)
		}
		out, err := json.Marshal(f)
		if err != nil {
			t.Fatalf("MarshalJSON: %v", err)
		}
		var a, b map[string]any
		_ = json.Unmarshal([]byte(in), &a)
		if err := json.Unmarshal(out, &b); err != nil {
			t.Fatalf("re-read: %v", err)
		}
		if _, isNum := a["id"].(float64); isNum {
			if _, stillNum := b["id"].(float64); !stillNum {
				t.Errorf("%s: a numeric id came back as %T", in, b["id"])
			}
			continue
		}
		if _, isStr := b["id"].(string); !isStr {
			t.Errorf("%s: a string id came back as %T", in, b["id"])
		}
	}
}

func TestJSON_Kinds(t *testing.T) {
	for _, tc := range []struct {
		in   string
		kind Kind
	}{
		{`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`, Request},
		{`{"jsonrpc":"2.0","method":"notifications/initialized"}`, Notify},
		{`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`, Response},
		{`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"nope"}}`, Response},
	} {
		var f Frame
		if err := json.Unmarshal([]byte(tc.in), &f); err != nil {
			t.Fatalf("%s: %v", tc.in, err)
		}
		if f.Kind != tc.kind {
			t.Errorf("%s: kind = %s, want %s", tc.in, f.Kind, tc.kind)
		}
	}

	var f Frame
	if err := json.Unmarshal([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"nope"}}`), &f); err != nil {
		t.Fatal(err)
	}
	if f.Err == nil || f.Err.Code != CodeMethod {
		t.Fatalf("error not read: %+v", f.Err)
	}
	if f.Result != nil {
		t.Errorf("a failure also carried a result: %s", f.Result)
	}
}

// JSON-RPC requires a successful response to CARRY a result member. Omitting it
// for a void call makes the message a malformed response rather than a void
// one, and strict clients reject it.
func TestJSON_VoidResultIsNull(t *testing.T) {
	b, err := json.Marshal(Frame{Kind: Response, ID: "1"})
	if err != nil {
		t.Fatalf("MarshalJSON: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if got, ok := m["result"]; !ok || string(got) != "null" {
		t.Errorf("void response = %s, want a null result member", b)
	}
}

// Answer and Fail are the ONE place a reply is correlated, so a handler cannot
// forget to copy the id across.
func TestFrame_AnswerCorrelates(t *testing.T) {
	req := full()
	for _, ans := range []*Frame{req.Answer([]byte(`1`)), req.Fail(CodeParams, "bad")} {
		if ans.Kind != Response || ans.ID != req.ID || ans.Session != req.Session || ans.Seq != req.Seq {
			t.Errorf("answer lost the correlation: %+v", ans)
		}
	}
}
