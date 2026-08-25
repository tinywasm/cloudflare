# Architecture — tinywasm/cloudflare

The Go/WASM runtime for Cloudflare Workers. This repo is the **library half** extracted from `tinywasm/goflare` — `goflare` keeps the build/deploy CLI and imports this repo only for the runtime it ships inside every Worker.

## Why the split

`tinywasm/goflare` mixed two lifecycles in one module:

1. **Runtime** — code that compiles into every deployed Worker (`edge/`, `workers/`, `d1/`, `r2/`, `log/`). Its surface must be stable: every change ships inside a 1 MB-capped binary, for every consumer, at once. Env is `tinywasm/env` (auto-tag `!wasm`=`os`+`.env`, `wasm`=`context.env`).
2. **Tooling** — CLI that builds and deploys (`goflare.go`, `build.go`, `javascripts.go`, `assets.go`, `cloudflare.go` API client, `config.go`, `mode.go`, `devserver/`). It changes constantly and legitimately uses `os/exec`, file I/O, HTTP — none of which may ever reach the runtime side.

Build tags kept them from cross-importing by accident, but one module meant one `go.mod`, one release cadence, and one directory to understand. Splitting makes the separation structural.

## Packages

| Package | Build tag | Role |
|---|---|---|
| `edge/` | `//go:build wasm` | Router adapter on top of `workers/` (`router.Router` → `workers.Handle`). Compiles middleware once (`compile()`), gates via `Validate()`/`allows()`, dispatches via `Dispatch()`/`Serve()`. |
| `workers/` | `//go:build wasm` | JS↔Go bridge (`Request`/`Response` ↔ JS `Request`/`Response` via `syscall/js`). Owns the isolate handshake (`Handle()`/`Ready()` via `context.binding`). |
| `d1/` | `//go:build wasm` | D1 adapter (`storage.Compiler` + `storage.Conn` over `context.env` binding) for `tinywasm/orm`. |
| `r2/` | `//go:build wasm` | R2 bucket adapter over `context.env` binding (`Put`/`Get`/`Delete`/`List`). |
| `log/` | `//go:build wasm` | Minimal edge logging — `Reject` (4xx), `Fail` (5xx), `Panic` (recovered at request boundary). |
| `assets/` | `//go:build !wasm` | JS half of the runtime (`wasm_exec_worker.js`, `runtime.mjs`, `worker.mjs`) embedded for `goflare`'s bundler (`assets/embed.go`). `!wasm`-tagged — a Worker never reads its own source as data. |

## Isolate lifecycle — the contract

A Worker's Go instance is started **once per isolate**, not per request (fixed 2026-08-25, `goflare` v0.5.15). Three invariants keep it true — see `AGENTS.md`:

1. **`js.Global()` only isolates `context` per instance.** `assets/wasm_exec_worker.js`'s Proxy resolves `js.Global().Get("context")` per instance; every other global name is the single `globalThis` shared by all concurrent requests. Never coordinate via a bare `globalThis` slot — route through `context.binding`.
2. **`main()` must signal readiness or fail loudly.** `assets/worker.mjs`'s `start()` observes `go.run`'s promise so a `main()` that returns early (missing binding/secret) throws a named error; otherwise every future request hangs with nothing logged.
3. **`runtime.ticks` ABI.** Must return `int64` nanoseconds as `BigInt`, matching TinyGo's `targets/wasm_exec.js`. Getting it wrong traps during package init, before `main()` runs, before anything can log. Diff against `$(tinygo env TINYGOROOT)/targets/wasm_exec.js` before touching `wasm_exec_worker.js`.

## JS runtime bundling

`assets/wasm_exec_worker.js` (TinyGo glue, IIFE-stripped), `assets/runtime.mjs` (`loadModule` + `createRuntimeContext`), and `assets/worker.mjs` (`fetch`/`scheduled`/`queue` + `export default`) are the JS half of the Go contract. They live here, not in `goflare`, and are exposed via `assets/embed.go` for `goflare`'s `javascripts.go` to bundle.

`__GOFLARE_VERSION__` in `worker.mjs` is left as text; `goflare`'s bundler does the substitution.

## Testing

```bash
go install github.com/tinywasm/devflow/cmd/gotest@latest
gotest
```

`gotest` (see `tinywasm/devflow/docs/GOTEST.md`) runs `go vet`, `go test -race -cover` y la suite WASM en un browser real vía `wasmbrowsertest` (auto-detectada por `//go:build wasm`, no por el backend `GOOS=js` del toolchain Go que acepta imports que TinyGo rechazaría). Concurrency bugs (shared-global handshake) no pueden tener un test timing-dependiente en browser — pruébalos estructuralmente (assert sobre el JS shipeado o comportamiento Go con `js.Value` fake).

No `internal/` folders.

## Project structure

```
cloudflare/
├── edge/               # router adapter
├── workers/            # JS↔Go bridge
├── d1/                 # D1 adapter
├── r2/                 # R2 adapter
├── log/                # edge logging
├── assets/             # JS runtime + embed.go
│   ├── wasm_exec_worker.js
│   ├── runtime.mjs
│   ├── worker.mjs
│   └── embed.go        # !wasm embed for tooling
├── tests/              # edge/workers/r2 conformance + runtime ABI tests
└── docs/
    ├── ARCHITECTURE.md
    └── LAST_PLAN_EXECUTED.md
```
