---
PLAN: "feat: d1.NewMigrator — execute DDL against D1 from outside a Worker"
EXECUTOR: jules
REVIEWER: none
STATUS: running
SESSION: 13879164902895928035
---

> This plan is dispatched via the CodeJob workflow. See skill: agents-workflow.
>
> Read `AGENTS.md` first. It documents three non-obvious runtime constraints
> and one hard rule (no tooling in this repo). This plan does not violate
> that rule — see "Why it lives here" below, which explains why a second D1
> *transport* is runtime surface, not tooling.
>
> **BLOCKED — do not start until `tinywasm/ddl`'s own `docs/PLAN.md` is
> merged and published.** That plan widens `ddl.New` to accept an `Execer`
> (just `Exec`) instead of a full `storage.Conn`. Without it, the connection
> below has to declare `Query`/`QueryRow` and return errors from them — a
> contract that lies, which `CONSTRUCTION_HARNESS.md` forbids ("illegal
> states unrepresentable"). Bump this repo's `ddl` dependency to that
> published version as the first action of Stage 1.

# Plan — `tinywasm/cloudflare`: `d1.NewMigrator`

## The measured problem this is one step of

`veltylabs/iam` answers cold-start requests in **1.4–10.4 s**. Measured, not
estimated — `main()` was instrumented with `Date.now()` deltas, deployed, and
the instrumentation reverted (see `veltylabs/iam` git history, 2026-08-25,
the two `debug:` commits):

```
timing: d1.NewEdge                0 ms
timing: unixid.NewUnixID          0 ms
timing: NewProductionBackend   8531 ms  /  10407 ms
```

`NewProductionBackend` runs three `ddl.Sync` calls (`authority.New`,
`rbac.New`, `initProjectSchema`) covering ~14 models. Each model costs at
least one D1 round trip, and Cloudflare recycles isolates constantly, so
real users pay those seconds regularly.

The fix is to run schema reconciliation **once at deploy time** instead of at
every isolate start. That needs a way to execute DDL against D1 from CI —
from outside a Worker, where the `NewEdge` binding does not exist. **This
plan adds only that.** Removing the runtime sync (`tinywasm/auth`,
`tinywasm/rbac`) and wiring the CI step (`veltylabs/iam`) are separate,
later plans — see `tinywasm/docs/CLOUDFLARE_RUNTIME_MASTER_PLAN.md`, Fase B.

## What this deliberately does NOT build

**`ddl.Sync` needs only `Exec`.** Verified by reading
`tinywasm/ddl/sync.go`: `syncModel` calls `CreateTable` and `execDDL`, both
of which end in `d.conn.Exec(q, args...)`. It touches `Compile`/`Query` only
in step 6 ("Reconcile Safe Drops", sync.go:180–185), which is unreachable
because step 2 returns early when the connection does not implement
`ddl.TableIntrospector`:

```go
	// 2. Cast conn to TableIntrospector
	introspector, ok := d.conn.(TableIntrospector)
	if !ok {
		// Fallback to purely additive loop
		for _, field := range schema { ... d.execDDL(...) }
		return nil
	}
```

The existing WASM adapter (`d1/adapter.go`, used by `NewEdge`) does **not**
implement `TableIntrospector` — confirm with
`grep -n "TableColumns" d1/*.go` → empty. So `iam` in production **already**
runs that additive path today. This plan changes the transport, not the
migration behavior.

Therefore: **no `storage.Rows`, no `storage.Scanner`, no column-order
handling, no type conversion.** An earlier draft of this plan built a full
ORM adapter (rows, scanners, `[]fmt.KeyValue` ordering, int/float/bool
coercion) and was discarded as duplication before anything was committed —
`sqlt` already generates the SQL and `ddl` already owns the reconciliation
logic. Do not reintroduce any of it. If schema *introspection* over HTTP is
ever wanted (for safe column drops), that is a separate, measured decision:
implement `ddl.TableIntrospector` then, not now.

## Why it lives here and not in `tinywasm/goflare`

This is a **transport for a Cloudflare product** — the same thing `NewEdge`
is, over a different wire. That is this repo's purpose. Precedent inside the
ecosystem:

- `tinywasm/crypto/rand` ships `rand_native.go` (`//go:build !wasm`) and
  `rand_wasm.go` (`//go:build wasm`) in one package, split only by target.
- This repo's own `docs/D1.md` **already documents** `NewLocal` — *"Host only
  (!wasm)"* — a non-WASM D1 constructor that was specified but never
  implemented. A second host-side constructor is the design this package was
  always meant to have.

`goflare` will *consume* this (its job is talking to Cloudflare); this repo
never imports `goflare`. The migration *command* that decides **which**
models to sync belongs to the application that owns them (`veltylabs/iam`),
not to `goflare` and not here.

## Stage 1 — `d1/remote.go` (new file, `//go:build !wasm`)

```go
//go:build !wasm

package d1

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/tinywasm/ddl"
	. "github.com/tinywasm/fmt"
)

// NewMigrator opens a D1 database over Cloudflare's HTTP Query API, for
// running schema migrations from CI — outside a Worker, where the NewEdge
// binding does not exist.
//
// It is named for what it does rather than for its transport, and its return
// type says the same thing: ddl.Execer is exactly "somewhere to send DDL",
// so this connection cannot be mistaken for one you can read rows from.
// Inside a Worker, use NewEdge.
//
// Because it is not a storage.Conn, ddl.Sync takes its additive path —
// CREATE TABLE plus ADD COLUMN, no safe-drop probe. That is the same path
// NewEdge already produces in production today (the WASM adapter does not
// implement ddl.TableIntrospector either), so migrating through this
// transport does not change migration behavior.
//
// Usage:
//
//	conn, err := d1.NewMigrator(accountID, databaseID, apiToken)
//	err = ddl.New(conn, sqlt.NewCompiler()).Sync(models...)
func NewMigrator(accountID, databaseID, apiToken string) (ddl.Execer, error) {
	if accountID == "" || databaseID == "" || apiToken == "" {
		return nil, Err(errPrefix + "NewMigrator requires accountID, databaseID and apiToken")
	}
	return newMigratorWithURL(
		"https://api.cloudflare.com/client/v4/accounts/"+accountID+"/d1/database/"+databaseID+"/query",
		apiToken,
	), nil
}

// newMigratorWithURL is the test seam — it lets tests point the client at an
// httptest.Server instead of the real Cloudflare API.
func newMigratorWithURL(url, apiToken string) *migratorConn {
	return &migratorConn{url: url, token: apiToken, client: http.DefaultClient}
}

// migratorConn carries no Compiler: ddl.New takes the DDL compiler as its
// second argument (sqlt.NewCompiler()), and without storage.Compiler this
// type cannot be mistaken for a queryable connection.
type migratorConn struct {
	url    string
	token  string
	client *http.Client
}

// Exec POSTs one statement to D1's Query API and reports whether it applied.
// Request:  {"sql": "...", "params": [...]}
// Response: {"success": bool, "errors": [{code,message}], "result": [...]}
func (m *migratorConn) Exec(query string, args ...any) error {
	params := make([]any, len(args))
	copy(params, args)
	body, err := json.Marshal(struct {
		SQL    string `json:"sql"`
		Params []any  `json:"params"`
	}{SQL: query, Params: params})
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, m.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var envelope struct {
		Success bool `json:"success"`
		Errors  []struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return Err(errPrefix + "could not decode the D1 response")
	}
	if !envelope.Success || resp.StatusCode >= 400 {
		if len(envelope.Errors) > 0 {
			// Name the real cause: a migration that fails with a generic
			// "request failed" is undiagnosable from a CI log.
			return Err(errPrefix + envelope.Errors[0].Message)
		}
		return Err(errPrefix + "the D1 query failed")
	}
	return nil
}

var _ ddl.Execer = (*migratorConn)(nil)
```

That is the whole type. No `Query`, no `QueryRow`, no `Close`, no
`storage.Rows`, no `storage.Scanner` — `ddl.Execer` (published by that
repo's plan) requires exactly one method, so there is nothing to fake. If
you find yourself adding any of them to satisfy a compiler error, the `ddl`
dependency was not bumped: stop and do that first.

Nothing needs to move out of `d1/adapter.go`: `CompileDDL` lives there
behind `//go:build wasm` and stays there, because this transport does not
carry a compiler at all — `ddl.New(conn, sqlt.NewCompiler())` takes it as
its second argument.

## Stage 2 — tests

New file `tests/d1_migrator_test.go` (`//go:build !wasm`, package
`cloudflare_test`), using `httptest.Server` — the same fixture-server pattern
`goflare`'s `tests/deploy_test.go` uses for its own Cloudflare API tests.
Cover:

1. **A successful `Exec`** — assert the request body carries the SQL under
   `"sql"` and that a `{"success": true, ...}` response yields `nil`.
2. **The authorization header** — assert the request arrives with
   `Authorization: Bearer <token>`.
3. **An error envelope** — a `{"success": false, "errors": [{"code": 7500,
   "message": "syntax error near 'SELCT'"}]}` response must produce an error
   whose text **contains `syntax error near 'SELCT'`**, not a generic
   failure string.
4. **An end-to-end `ddl.Sync`** — build `ddl.New(conn, sqlt.NewCompiler())`
   against the fixture server and `Sync` one small model. Assert the server
   received a `CREATE TABLE` and the per-field `ADD COLUMN` statements. This
   is the case that proves the whole point of the change: a connection that
   is *only* an `Execer` drives a real migration, with nothing faked.

Write fixture responses as **literal JSON strings**, never built from a Go
`map[string]any`: maps are banned in this ecosystem (`AGENTS.md`), and
`json.Marshal` sorts map keys, which silently reorders fields in a fixture.

## Stage 3 — docs

`docs/D1.md`: add `NewMigrator` to the "Public API" block next to `NewEdge`,
and replace the unimplemented `NewLocal` entry with it (or keep `NewLocal`
listed as still-unimplemented, but do not leave the file claiming an API
that does not exist). Include the three-line usage example from
`NewMigrator`'s doc comment and state plainly that it executes DDL only.

## Acceptance criteria

- [ ] `go build ./...` and `go vet ./...` clean (native).
- [ ] `GOOS=js GOARCH=wasm go build ./...` clean — `d1/remote.go` is `!wasm`
      and must not reach the Worker target.
- [ ] `gotest` green, including the four new cases.
- [ ] `grep -n "storage\." d1/remote.go` → **empty**. The file needs no
      `storage` import at all; if it does, the type is doing more than
      executing DDL.
- [ ] `grep -rn "map\[" d1/remote.go tests/d1_migrator_test.go` → empty.
- [ ] `d1/remote.go` declares exactly one method on `migratorConn` (`Exec`).

| Stage | File(s) | Done when |
|---|---|---|
| 1 | `d1/remote.go` | `NewMigrator` returns a `ddl.Execer` that executes DDL over HTTP; one method, nothing faked |
| 2 | `tests/d1_migrator_test.go` | Four cases green, including a real `ddl.Sync` against the fixture server |
| 3 | `docs/D1.md` | `NewMigrator` documented; no unimplemented API left claimed |

## Noted, deliberately out of scope

D1's Query API accepts **multiple statements joined by `;` as one batch**
(confirmed in Cloudflare's OpenAPI spec for this endpoint). `ddl.Sync` calls
`Exec` once per statement, so a migration of ~14 models costs ~14 round
trips. That is irrelevant in CI (once per deploy) and batching would require
buffering `Exec` calls plus a `Flush()` — real complexity for no measured
benefit here. Recorded so the option is not rediscovered as if new.
