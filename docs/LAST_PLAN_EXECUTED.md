---
PLAN: "feat: import the Cloudflare Workers runtime out of tinywasm/goflare"
---

> This plan runs LOCALLY, by an agent other than the one that wrote it — not
> dispatched via CodeJob. On start, rename this file to
> `docs/LAST_PLAN_EXECUTED.md` per the Local Execution Flow (skill:
> agents-workflow) and execute from there. Publish with `gopush`, never
> `codejob` — there is no PR/dispatch loop for this plan.
>
> Read `AGENTS.md` in this repo before writing any code — it documents three
> non-obvious constraints (the isolate lifecycle, the `runtime.ticks` ABI,
> the shared-global concurrency bug) that a fresh agent would otherwise
> rediscover the hard way, because each one shipped to production before it
> was found.

# Plan — `tinywasm/cloudflare`: import the runtime out of `goflare`

## Why

`tinywasm/goflare` today mixes two things that don't share a lifecycle:

1. **The runtime** — code that compiles into every deployed Worker
   (`edge/`, `workers/`, `d1/`, `r2/`, `cloudflare/{env_native,env_wasm}.go`,
   `log/`). Its surface should be as stable as possible: every change here
   ships inside a 1 MB-capped binary, for every consumer, at once.
2. **The tooling** — the CLI that builds and deploys a Worker
   (`goflare.go`, `build.go`, `wasm.go`, `javascripts.go`, `assets.go`,
   `cloudflare.go` the API client, `config.go`, `mode.go`, `pages.go`,
   `store.go`, `devserver/`, `cmd/goflare/`). It changes constantly and
   legitimately uses `os/exec`, file I/O, HTTP clients — none of which may
   ever reach the runtime side.

Go's build tags already keep them from cross-importing by accident today
(the runtime is `//go:build wasm`, the tooling `//go:build !wasm`), but they
still live in one module, one `go.mod`, one release cadence, and one
directory a contributor has to understand as a whole. Splitting them into
two repos makes that separation structural instead of a convention someone
has to remember — see `CONSTRUCTION_HARNESS.md`, "things you have to
remember… close it with types or a single path, not with prose."

This repo has **zero consumers today** — nothing imports
`github.com/tinywasm/cloudflare` yet, so every decision below is free to
break freely. `goflare` and its own consumers (`veltylabs/iam`,
`veltylabs/misitio`, `tinywasm/goflare-demo`, `tinywasm/app`) update
afterward, in their own separate one-line-import plans — not part of this
one.

## Stage 1 — copy the runtime packages, verbatim except one rename

Copy these directories from `tinywasm/goflare` (path:
`/home/cesar/Dev/Project/tinywasm/goflare`) into this repo, unchanged except
package declarations where noted:

| From (`goflare`) | To (`cloudflare`) | Change |
|---|---|---|
| `edge/*.go` | `edge/*.go` | none |
| `workers/*.go` (incl. `response_internal_test.go`, `request_internal_test.go`) | `workers/*.go` | none |
| `d1/*.go` | `d1/*.go` | none |
| `r2/*.go` | `r2/*.go` | none |
| `log/log.go` | `log/log.go` | none |
| `cloudflare/env_native.go`, `cloudflare/env_wasm.go` | `env_native.go`, `env_wasm.go` (repo root) | **`package cloudflare` subpackage → repo-root package.** The subpackage name collided with the new repo's own name (`tinywasm/cloudflare/cloudflare`, awkward and redundant); at the root it reads as `cloudflare.Env`, exactly like it does today. |

Every import inside these files that currently reads
`github.com/tinywasm/goflare/<pkg>` (e.g. `edge/edge.go` importing
`github.com/tinywasm/goflare/workers`, `github.com/tinywasm/goflare/log`)
becomes `github.com/tinywasm/cloudflare/<pkg>`. `cloudflare.Env` references
(`d1/adapter.go` does not use it, but check every file) become the
repo-root import `github.com/tinywasm/cloudflare` with no subpackage
suffix.

Do not carry over anything from `goflare`'s tooling side — no
`goflare.go`, `build.go`, `wasm.go`, `javascripts.go`, `assets.go` (the
tooling file — assets themselves move in Stage 2), `cloudflare.go` (the API
client), `config.go`, `mode.go`, `pages.go`, `store.go`, `auth.go`,
`events.go`, `run.go`, `devtui.go`, `devserver/`, `cmd/`.

## Stage 2 — move the JS runtime, expose it for `goflare` to embed

The three JS files under `goflare/assets/` are the JS half of this same
runtime — `worker.mjs`/`runtime.mjs` are what `main.go`'s wasm binary talks
to via `syscall/js`, and `wasm_exec_worker.js` is the TinyGo-glue AGENTS.md
now documents ABI constraints for. They belong with the Go code they're a
contract with, not with the tool that happens to bundle them.

Move `assets/wasm_exec_worker.js`, `assets/runtime.mjs`, `assets/worker.mjs`
into this repo at `assets/` (same three filenames, same content — the
`__GOFLARE_VERSION__` placeholder in `worker.mjs` stays exactly as text;
`goflare`'s bundler still does the substitution, see Stage 3).

Add `assets/embed.go`:

```go
//go:build !wasm

// Package assets embeds the JS half of the Cloudflare Workers runtime, for
// goflare (or any other bundler) to read without vendoring a copy. The Go
// side of this contract lives in workers/ and edge/ — see AGENTS.md.
package assets

import _ "embed"

//go:embed wasm_exec_worker.js
var WasmExecJS []byte

//go:embed runtime.mjs
var RuntimeMJS []byte

//go:embed worker.mjs
var WorkerMJS []byte
```

`!wasm`-tagged because nothing inside a Worker ever reads its own source as
data — this is purely a tooling-side embed, consumed by `goflare`'s
bundler, not by the runtime itself.

## Stage 3 — point `goflare` at the new repo (companion change, same PR is fine)

In `/home/cesar/Dev/Project/tinywasm/goflare`:

- Delete `edge/`, `workers/`, `d1/`, `r2/`, `log/`, `cloudflare/`,
  `assets/wasm_exec_worker.js`, `assets/runtime.mjs`, `assets/worker.mjs`.
- `go get github.com/tinywasm/cloudflare@<published version>`.
- `javascripts.go`: replace the three `//go:embed assets/...` declarations
  and their `var embeddedX []byte` with reads from
  `cloudflareassets "github.com/tinywasm/cloudflare/assets"` —
  `cloudflareassets.WasmExecJS`, `.RuntimeMJS`, `.WorkerMJS` — everywhere
  `embeddedWasmExec`/`embeddedRuntime`/`embeddedWorker` were used.
- `docs/BUILD_WORKER_ASSETS.md`, `docs/ARCHITECTURE.md`: update any mention
  of `assets/worker.mjs` etc. living in this repo to point at
  `tinywasm/cloudflare` instead.
- `AGENTS.md`: remove whatever runtime-lifecycle guidance duplicates what
  now lives in `tinywasm/cloudflare/AGENTS.md` — link to it instead of
  restating it, so the two copies can't drift.

Do **not** touch `veltylabs/iam`, `veltylabs/misitio`,
`tinywasm/goflare-demo`, `tinywasm/app` here — their import-path updates
are separate, one-line plans, written after this one is published (see
`tinywasm/docs/CLOUDFLARE_RUNTIME_MASTER_PLAN.md`, Fase A2).

## Stage 4 — tests

Test files split by what they actually exercise, not by which directory
they happened to live in — `goflare/tests/` today mixes library tests with
tooling tests in one package. Grep-verify with:

```bash
grep -l 'goflare/edge\|goflare/workers\|goflare/d1\|goflare/r2\|goflare/cloudflare"' tests/*.go
```

Move (rewriting the import path) if the test's *subject* is the library —
confirmed for these:

- `tests/edge_conformance_test.go`, `tests/edge_helpers_test.go`,
  `tests/ready_handshake_test.go`, `tests/worker_runtime_test.go`,
  `tests/wasm_exec_abi_test.go`, `tests/edge_middleware_compile_test.go` →
  this repo's `tests/` (edge/workers/runtime behavior).
- `tests/r2_test.go` → this repo's `tests/` (`r2.NewEdge` round-trip).

**Leave in `goflare`** (rewrite the import to
`github.com/tinywasm/cloudflare/...` but the test itself stays — it
exercises `goflare`'s own build/deploy, using the runtime only as a fixture):
`tests/deploy_verify_test.go`, `tests/build_worker_assets_test.go`,
`tests/edge_size_test.go` (calls `goflare.EnsureTinyGo`, a tooling
function), `tests/files_test.go` if it tests upload/download tooling rather
than `r2` itself — verify each one's actual subject before moving it; the
grep above only tells you it *mentions* a moving package, not which repo it
belongs in.

`workers/response_internal_test.go` and `workers/request_internal_test.go`
move with `workers/` as-is (same package, `package workers`, needs no
import rewrite).

## Stage 5 — docs

- `docs/ARCHITECTURE.md` (new, this repo): describe the split from
  `goflare`, the isolate-once-per-lifecycle contract, and the package list
  from Stage 1's table.
- `README.md`: replace the `gonew` placeholder with real usage — `go get
  github.com/tinywasm/cloudflare/edge` (etc.), a minimal `main.go` example
  mirroring `veltylabs/iam/edge/main.go`'s shape.

## Acceptance criteria

- [ ] `go build ./...` and `go vet ./...` clean (both `GOOS=js GOARCH=wasm`
      for the wasm-tagged packages and native for `assets/embed.go`).
- [ ] `GOOS=js GOARCH=wasm go test -exec wasmbrowsertest ./...` green.
- [ ] `grep -rn "github.com/tinywasm/goflare" .` in this repo → empty.
- [ ] In `goflare`: `grep -rn "github.com/tinywasm/cloudflare" .` → present
      only in `javascripts.go` and `go.mod`/`go.sum`.
- [ ] `goflare`'s own suite (`go test ./tests/...` and the wasm suite) still
      green after Stage 3 — a real `goflare build` on a fixture project
      must still produce a working `edge.js`/`edge.wasm` pair.
- [ ] Publish with `gopush` (both repos), not `codejob` — see the header
      note.

| Stage | Repo | Done when |
|---|---|---|
| 1 | `tinywasm/cloudflare` | `edge`/`workers`/`d1`/`r2`/`log`/root-`cloudflare` package copied, imports rewritten |
| 2 | `tinywasm/cloudflare` | JS runtime moved to `assets/`, exposed via `assets/embed.go` |
| 3 | `tinywasm/goflare` | Moved packages deleted; `javascripts.go` reads from `tinywasm/cloudflare/assets` |
| 4 | both | Tests split by subject, all passing in their new home |
| 5 | both | Docs updated, no stale path references |
