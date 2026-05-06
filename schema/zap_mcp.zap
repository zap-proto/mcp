# zap-mcp — Model Context Protocol over ZAP.
#
# MCP is a JSON-RPC 2.0 protocol for AI agents calling tool servers.
# zap-mcp wraps each JSON-RPC message in a ZAP frame, signed with the
# calling agent's ML-DSA-65 key. Verifiers years later can reconstruct
# whether a given agent issued a given call without trusting any
# operator log or HTTP middleware.
#
# See zap-proto/papers/agent-communication for the non-repudiation
# theorem and concrete bounds.

# Frame is the unit on the wire. Either a JSON-RPC request or response,
# wrapped with provenance (sigSubject, sig, sessionId, seqNum).
struct Frame
  sessionId  Text
  seqNum     UInt64
  sigSubject Text       # which agent's key signed this
  sig        Data       # ML-DSA-65 signature over canonical(payload || envelope)
  payload    Payload

struct Payload
  union
    request  Request
    response Response
    notify   Notify

# Request matches the JSON-RPC 2.0 shape.
struct Request
  id     Text
  method Text
  params Data           # opaque JSON or ZAP binary, content-type in `kind`
  kind   ParamKind

# Response matches the JSON-RPC 2.0 shape; exactly one of result/error.
struct Response
  id     Text
  result Data
  error  Error
  kind   ParamKind

struct Error
  code    Int32
  message Text
  data    Data

struct Notify
  method Text
  params Data
  kind   ParamKind

enum ParamKind
  json
  zapBinary
  none
