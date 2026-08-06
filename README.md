# @zap-proto/mcp

> **Docs:** [Model Context Protocol over ZAP](https://zap-proto.dev/docs/protocols/mcp) · part of the [ZAP Protocol](https://zap-proto.io)

Model Context Protocol over ZAP — mutually authenticated, non-repudiable AI agent ↔ tool communication.

[**zap-proto.io**](https://zap-proto.io) · [Spec](https://github.com/zap-proto/spec) · [Paper](https://github.com/zap-proto/papers/tree/main/agent-communication) · [Discord](https://zap-proto.io/discord)

`@zap-proto/mcp` layers AI agent ↔ tool semantics over the [ZAP wire layer](https://github.com/zap-proto/ts). Post-quantum confidentiality, mutual authentication, and zero-copy parse come from the wire ([`@zap-proto/zap`](https://www.npmjs.com/package/@zap-proto/zap)); this package adds only the AI agent ↔ tool message shape: clients, servers, tool calls, and the transport that carries them.

## Install

```sh
pnpm add @zap-proto/mcp
```

`@zap-proto/zap` (the ZAP wire layer) is a dependency and is installed automatically.

## Usage

```ts
// MCP server
import { ZapServer } from "@zap-proto/mcp/server";

const server = new ZapServer({ port: 9999 });
server.tool("search", "Search the web", {}, async (params) => ({
  content: [{ type: "text", text: "results" }],
}));
await server.start();
```

```ts
// Browser extension client
import { ExtensionClient } from "@zap-proto/mcp/browser";

const client = new ExtensionClient({ browser: "chrome", version: "1.0.0" });
const mcps = await client.discover();
const result = await client.callTool("search", { query: "hello" });
```


## Go

```sh
go get github.com/zap-proto/mcp
```

`github.com/zap-proto/mcp` is the sibling of [`zap-proto/http`](https://github.com/zap-proto/http):
same seams, same relationship to the ZAP core, neither depending on the other.

```go
// server — no HTTP listener anywhere in the path
srv := &zapmcp.Server{Network: "unix", Addr: "/run/zip/tools.sock", Handler: h}
srv.ListenAndServe()

func h(ctx context.Context, f *zapmcp.Frame) *zapmcp.Frame {
        if f.Method != "tools/call" {
                return f.Fail(zapmcp.CodeMethod, "method not found: "+f.Method)
        }
        return f.Answer(result)
}

// client
c := zapmcp.Dial("unix", "/run/zip/tools.sock")
out, err := c.Call(ctx, "tools/call", args)
```

### One value, two projections

MCP is JSON-RPC 2.0; ZAP is binary. Rather than two message types kept in step
by hand there is one — `Frame` — and two renderings of it:

| | |
|---|---|
| `zapmcp.Marshal(f)` | the ZAP wire ([`schema/zap_mcp.zap`](schema/zap_mcp.zap)) |
| `json.Marshal(f)` | the JSON-RPC 2.0 message |

A server answers frames and never learns which door a frame came in, which is
what lets one handler serve ZAP natively and have HTTP be an adapter over it
rather than a peer of it.

## Layering

| Layer | Package | Owns |
|---|---|---|
| Application | `@zap-proto/mcp` (TS) · `github.com/zap-proto/mcp` (Go) | MCP message shape — tools, calls, agent ↔ tool semantics |
| Wire | [`@zap-proto/zap`](https://github.com/zap-proto/js) | ZAP zero-copy wire codec + Level-1 RPC primitives |

By the [composability theorem](https://github.com/zap-proto/papers/tree/main/composability), `@zap-proto/mcp` inherits ZAP's PQ confidentiality and mutual auth from the wire — no MCP-specific PQ analysis required.

## Why

| Property | MCP-over-HTTP+SSE / stdio | `@zap-proto/mcp` |
|---|---|---|
| Confidentiality | TLS (classical) | X-Wing hybrid PQ (default) |
| Authentication | bearer / TLS cert | KEM keypair at transport |
| Wire encoding | text or per-protocol binary | ZAP wire, zero-copy |
| Identity binding | DNS / cert chain | [zap-rns](https://github.com/zap-proto/rns) keypair |
| Future-quantum | classical only | hybrid by construction |

## Sub-protocol family

- [`zap-http`](https://github.com/zap-proto/http) — HTTP request/response over ZAP
- [`zap-ws`](https://github.com/zap-proto/ws) — multi-stream pubsub
- [`zap-fix`](https://github.com/zap-proto/fix) — FIX 4.4 / 5.0 trading channel
- [`zap-rns`](https://github.com/zap-proto/rns) — KEM-bound service naming
- [`zap-mcp`](https://github.com/zap-proto/mcp) — Model Context Protocol over ZAP
- [`zap-acp`](https://github.com/zap-proto/acp) — Agent Communication Protocol
- [`zap-a2a`](https://github.com/zap-proto/a2a) — Google Agent2Agent over ZAP

## License

MIT OR Apache-2.0
