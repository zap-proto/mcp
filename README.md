# zap-mcp

> **Docs:** [Model Context Protocol over ZAP](https://zap-proto.dev/docs/protocols/mcp) · part of the [ZAP Protocol](https://zap-proto.io)


Model Context Protocol over ZAP — mutually authenticated, non-repudiable.

[**zap-proto.io**](https://zap-proto.io) · [Spec](https://github.com/zap-proto/spec) · [Paper](https://github.com/zap-proto/papers/tree/main/agent-communication) · [Discord](https://zap-proto.io/discord)

`zap-mcp` layers AI agent ↔ tool semantics over the [ZAP transport](https://github.com/zap-proto/spec). Post-quantum confidentiality, mutual authentication, and zero-copy parse come from the wire; this repo only adds the AI agent ↔ tool message shape.

## Status

**v0.1 — schema-first.** This repo currently ships:

- [`schema/zap_mcp.zap`](schema/zap_mcp.zap) — wire format spec in ZAP schema language

Reference implementations (Go, Rust, TS) land in v0.2 once `zap-proto/spec` provides cross-language codegen for the schema.

## Why

| Property | MCP-over-HTTP+SSE / stdio | `zap-mcp` |
|---|---|---|
| Confidentiality | TLS (classical) | X-Wing hybrid PQ (default) |
| Authentication | bearer / TLS cert | KEM keypair at transport |
| Wire encoding | text or per-protocol binary | ZAP wire, zero-copy |
| Identity binding | DNS / cert chain | [zap-rns](https://github.com/zap-proto/rns) keypair |
| Future-quantum | classical only | hybrid by construction |

By the [composability theorem](https://github.com/zap-proto/papers/tree/main/composability), `zap-mcp` inherits ZAP-base's PQ confidentiality and mutual auth automatically — no mcp-specific PQ analysis required.

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