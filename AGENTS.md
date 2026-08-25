# Agent Guide — `tinywasm/cloudflare`

Constraints for agents working on this library. Read this before any change.

---

## What this library is

The Go/WASM runtime for Cloudflare Workers: the JS↔Go bridge (`workers/`),
the router adapter built on it (`edge/`), and the storage adapters (`d1/`,
`r2/`). It is the **library half** of what used to be `tinywasm/goflare` —
`goflare` keeps the build/deploy CLI and imports this repo only for the
runtime it ships inside every Worker.

## The split it came from — why it matters

Before this repo existed, `goflare` mixed this runtime with its own
build/deploy tooling in one module. Two consequences that motivated the
split, and that a change here must not reintroduce:

1. **Backend tooling could leak into the edge binary by accident** — a
   `//go:build wasm` file in the same module as `os/exec`-using CLI code is
   one bad import away from dragging stdlib into a 1 MB-capped Worker.
2. **The runtime's surface should be stable; the tooling's should not.**
   Mixing them meant every CLI feature churned the file a Worker's
   correctness depends on.

**Consequence for this repo: no tooling code, ever, regardless of build
tag.** This repo is a library that `goflare` depends on — never the other
way around, and never side by side. If a change needs `os/exec`, an HTTP
client, CLI flag parsing, deploy/migration orchestration, or anything else
that only ever runs on a developer's machine or in CI rather than inside a
Worker, **it belongs in `goflare`, in a new file there** — not here behind a
`//go:build !wasm` tag. A `!wasm` tag changes *which target compiles a file*;
it says nothing about whether the file's *purpose* is runtime or tooling,
and it is not a license to put tooling here. (A prior version of this repo's
own `docs/PLAN.md` got exactly this wrong — proposed a host-side D1 HTTP
client, for CI migration tooling, living in this repo's `d1/` package. It
was caught before merging and the work was redirected to `goflare` instead.
Don't repeat it.)

**Second occurrence, 2026-08-25:** the same shape was proposed again —
`d1.NewMigrator`, a `net/http` client for D1 CI migrations, in `d1/remote.go`
behind `//go:build !wasm` — with a "transport, not tooling" justification
that this file already rebuts (a build tag says nothing about purpose). This
time review missed it: the plan was dispatched, the PR merged, and
`tinywasm/cloudflare v0.0.3` published with it, before it was caught and
reverted. If a plan proposes an HTTP client, `os/exec`, or CLI logic behind
`!wasm` in this repo, reject it on sight — do not evaluate its framing.
`goflare/cloudflare.go` already has `CfClient` for exactly this: Bearer auth
and `{success,errors,result}` envelope parsing. Reuse it there.

## No stdlib in `//go:build wasm` files

Use `github.com/tinywasm/fmt` instead of `fmt`/`errors`/`strconv`/`strings`.
Verify with `GOOS=js GOARCH=wasm go list -deps ./<pkg>/` before shipping —
`go list` under the *Go* toolchain accepts imports TinyGo will reject or that
silently bloat the binary. `crypto/*` stdlib is a documented exception in
`tinywasm/crypto` — it does not apply here; this repo has no reason to touch
crypto directly.

## The isolate lifecycle — read this before touching `workers/` or `edge/Serve`

A Worker's Go instance is started **once per isolate**, not once per request
(fixed 2026-08-25, `goflare` v0.5.15+ — see that repo's git history for the
production incident this closed). Three invariants keep that true; breaking
any of them reintroduces either a request-hang bug or a per-request cost
regression:

1. **`js.Global()` only isolates the `"context"` property per instance.**
   `assets/wasm_exec_worker.js`'s Proxy (`run(instance, context)`) resolves
   `js.Global().Get("context")` to the specific `context` closed over by
   *this* instance's `run()` call — every other global name falls through to
   the one real `globalThis`, shared by every concurrent request in the
   isolate. **Never coordinate anything through a bare `globalThis` slot** —
   route it through `context.binding`, the same door `Handle()` already uses
   for `handleRequest`. A shared-global handshake is exactly the bug that got
   fixed here; it is not a hypothetical.
2. **`main()` must always signal readiness or fail loudly — never just
   return.** `assets/worker.mjs`'s `start()` observes the promise `go.run`
   returns (which settles when Go's `main()` exits) specifically so a
   `main()` that returns early — a missing binding, a missing secret — throws
   a named error instead of leaving every future request in the isolate
   hanging forever with nothing logged. Don't strip that observation to
   "simplify" the JS glue.
3. **`runtime.ticks` must return `int64` nanoseconds as a `BigInt`, matching
   TinyGo's own `targets/wasm_exec.js` exactly.** Getting the ABI wrong here
   doesn't error — it traps the module during package init, before `main()`
   runs, before anything can log. Every Worker that reads the clock
   (`time.Now()`, `unixid`, `jwt`) hits it. If you ever touch
   `assets/wasm_exec_worker.js`, diff it against the TinyGo-installed copy
   (`$(tinygo env TINYGOROOT)/targets/wasm_exec.js`) for the current TinyGo
   version before assuming a stock function is correct.

## Testing

`workers`/`edge`/`d1`/`r2` are `//go:build wasm` — they run in a real browser
(via `wasmbrowsertest` under the hood, not the Go toolchain's `GOOS=js` backend
which accepts imports TinyGo would reject). Use `gotest` (ecosystem runner,
see `tinywasm/devflow/docs/GOTEST.md`):

```bash
go install github.com/tinywasm/devflow/cmd/gotest@latest
gotest              # vet + race + cover + wasm + badges
gotest -run TestFoo # filtro por nombre
```

`gotest` auto-detecta WASM por build tag `//go:build wasm`, no por nombre de archivo.

No headless-browser or Node harness exists in this ecosystem for driving
*two overlapping* `worker.mjs` invocations (concurrency bugs like #1 above
can't get a timing-dependent regression test here) — prove those structurally
(assert on the shipped JS source, or on Go-side behavior with a fake
`js.Value` fixture) rather than fabricating a test that can't actually fail.
See `tinywasm/goflare`'s `tests/ready_handshake_test.go` and
`tests/worker_runtime_test.go` for the pattern this produced.

## No `internal/` folders

Signature of a forked dependency instead of a contribution upstream — see
`tinywasm/app-releases/docs/CONSTRUCTION_HARNESS.md`.
