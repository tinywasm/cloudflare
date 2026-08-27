---
PLAN: "feat: path parameters in the edge router"
EXECUTOR: jules
REVIEWER: none
STATUS: review
SESSION: 13540689755628478172
PR: https://github.com/tinywasm/cloudflare/pull/3
---

> This plan is dispatched via the CodeJob workflow. See skill: agents-workflow.

# Plan — path parameters in the edge router

## Why this exists

`edge/edge.go` matches a route by prefix and nothing else:

```go
func pathMatches(pattern, pathname string) bool {
	if pattern == "" {
		return false
	}
	if pattern[len(pattern)-1] != '/' {
		return pathname == pattern
	}
	// ... byte-by-byte prefix compare
}
```

A Worker with a resource tree therefore cannot register
`/api/sites/{id}/content`. It registers `/api/sites/` four times — once per HTTP
verb, each declaring a different permission — and dispatches by inspecting the
path suffix **inside the handler**. That is a real deployment, not a
hypothetical: it is what `veltylabs/misitio` does today.

Two consequences, both bad:

1. **`Routes()` misreports the API.** It lists `/api/sites/` four times and
   never mentions `/content` or `/assets`. Everything that reads the route table
   — an operator, an MCP tool list, the introspection endpoint — is reading
   something that is not the API.
2. **Permissions are declared at the wrong granularity.** All four `/api/sites/`
   registrations gate the *same* prefix, so the permission attached to `PUT`
   guards every `PUT` under the subtree regardless of what it actually does.

`tinywasm/router` now owns the pattern syntax, the matcher and the ordering
rule. This plan makes the edge runtime use them.

## Dependency — read before you start

This plan requires **`github.com/tinywasm/router`** at the version exporting
`ParamNames`, `ValidatePattern`, `MatchPattern`, `MoreSpecific` and
`Context.Param`. It is **already published** when this plan is dispatched.

```
go get github.com/tinywasm/router@latest
go get github.com/tinywasm/model@latest
go mod tidy
```

**Never** add a `replace` directive, never invent a version, and never define
the helpers locally "until they land". If `go get` does not yield a `router`
with `MatchPattern`, stop and report it.

Adding `Param` to `router.Context` is a **breaking interface change**:
`*wasmContext` stops compiling until Stage 1 is done. That is expected. Do not
work around it with a type assertion or an embedded base struct.

## Anti-footguns

- Everything in `edge/` compiles with **TinyGo into a 1 MB-capped Worker**.
  No `map[K]V`. No `strings`/`strconv`/`errors`/stdlib `fmt` — use
  `tinywasm/fmt`. No `reflect`, not even transitively.
- **Do not re-implement matching here.** Every matching decision comes from
  `tinywasm/router`. Two copies of a matching rule is exactly the divergence
  this wave exists to remove.
- Do **not** add an `Introspection` or `RoutesEndpoint` field to `edge.Config`.
  `edge.NewRouter` returns a `router.Router`, so an app mounts the endpoint with
  one line — `router.MountIntrospection(r, router.IntrospectionPath, policy).Requires(...)`
  — and declares its own access. A boolean here would move that decision into
  the library, where it cannot see who is allowed to read the permission map.
- `edge/edge.go` is one file by design. Keep it that way; do not split it.

---

## Stage 1 — `wasmContext.Param`

File: **`edge/edge.go`**.

Add two parallel slices to `wasmContext` — **not a map**:

```go
type wasmContext struct {
	req         *workers.Request
	res         *workers.Response
	path        string
	vals        *context.Context
	uid         string
	paramNames  []string
	paramValues []string
}

// Param returns a path parameter the matched route declared with {name}.
//
// Backed by two parallel slices rather than a map: TinyGo compiles maps badly
// and inflates the binary, and a route never declares more than a handful of
// parameters, so a linear scan is cheaper than the hash anyway. Same reasoning
// as tinywasm/context, which backs SetValue/Value.
func (c *wasmContext) Param(name string) string {
	for i := 0; i < len(c.paramNames); i++ {
		if c.paramNames[i] == name {
			return c.paramValues[i]
		}
	}
	return ""
}
```

Parameters live in their **own** store, never in `vals`: `Value` is what a
middleware wrote, `Param` is what the URL carried, and collapsing them lets a
middleware forge a path parameter by writing a context value.

## Stage 2 — Delegate matching to `tinywasm/router`

Same file.

1. **Delete `pathMatches` entirely.** `grep -n "pathMatches" .` must come back
   empty.
2. In `match`, replace the `pathMatches` call with `router.MatchPattern`, and
   keep the values it returns for the winning route:

```go
func (r *wasmRouter) match(method, pathname string) (*wasmRoute, []string, int) {
	var best *wasmRoute
	var bestValues []string
	pathExists := false

	for _, rt := range r.routes {
		values, ok := router.MatchPattern(rt.info.Path, pathname)
		if !ok {
			continue
		}
		pathExists = true
		if rt.info.Method != "" && rt.info.Method != method {
			continue
		}
		if best == nil || router.MoreSpecific(rt.info.Path, best.info.Path) {
			best, bestValues = rt, values
		}
	}

	if best != nil {
		return best, bestValues, 200
	}
	if pathExists {
		return nil, nil, 405
	}
	return nil, nil, 404
}
```

The old tie-break was `len(rt.info.Path) > len(best.info.Path)`. That rule is
now wrong: `/api/sites/{id}` is 16 characters and `/api/sites/new` is 14, so the
parameter would beat the literal. `router.MoreSpecific` encodes the correct
three-clause ordering; **use it, do not re-derive it.**

3. Preserve the existing `405` vs `404` distinction exactly as it is. A path
   that exists for another method must keep answering `405`.

## Stage 3 — Validate patterns at registration

Same file, in `(*wasmRouter).Handle` — the single funnel every registration
method goes through.

```go
if err := router.ValidatePattern(path); err != nil {
	panic(err.Error())
}
```

A panic, not a returned error: `Handle` returns a `router.Route`, registration
happens at Worker startup, and a pattern the matcher can never satisfy is a
route that exists in the table and answers `404` forever. This repo already
refuses to start on a route that declares no access (`validateRoutes`) for the
same reason — be as loud here.

Apply it in `PublicAsset` and `PublicDir` too. A `PublicDir` prefix containing
`{` is a mistake, not a feature.

## Stage 4 — Wire the values into the request

Same file, in `gateAndServe`:

```go
func (r *wasmRouter) gateAndServe(ctx router.Context) {
	method, pathname := ctx.Method(), ctx.Path()

	route, values, status := r.match(method, pathname)
	if route == nil {
		// ... unchanged
	}

	if wc, ok := ctx.(*wasmContext); ok {
		wc.paramNames = router.ParamNames(route.info.Path)
		wc.paramValues = values
	}

	if ok, why := r.allows(route.info, ctx.UserID()); !ok {
		// ... unchanged
	}
	// ... unchanged
}
```

Parameters are set **before** the gate, so a middleware and an authorizer can
read them. The gate's position relative to the middleware chain does not change
— that ordering is load-bearing and documented in `edge.Config`; do not touch
it.

## Stage 5 — Tests

Under **`tests/`** (this repo's convention).

The conformance suite in `tinywasm/router` already contains the seven parameter
clauses and this repo already runs it from
`tests/edge_conformance_test.go`. **Verify that file still compiles and passes
unchanged** — if the suite's new clauses fail, the bug is in Stages 1–4, not in
the suite.

Add **`tests/edge_params_test.go`** for the edge-specific behaviour conformance
cannot express:

| Test | Assert |
|---|---|
| `TestEdgeParamsAreNotContextValues` | after matching `/api/items/{id}` on `/api/items/7`, `ctx.Value("id")` is `""` while `ctx.Param("id")` is `"7"` |
| `TestEdgeParamsResetPerRequest` | two sequential requests through the same router leave no value from the first visible in the second |
| `TestEdgeRegistrationRejectsWildcard` | `r.Get("/x/{a...}", h)` panics with the message `router.ValidatePattern` returns |
| `TestEdgeLiteralBeatsParameterAcrossMethods` | `/api/items/new` (GET) and `/api/items/{id}` (GET) both registered → `/api/items/new` reaches the literal handler |
| `TestEdgeSubtreeRouteStillMatches` | `/assets/` still matches `/assets/a/b/c` with no parameters — the pre-existing behaviour is intact |

Reuse the harness in `tests/edge_helpers_test.go`; do not build a second one.

## Stage 6 — Documentation

- **`docs/ARCHITECTURE.md`** — in the `edge/` section, state that matching,
  pattern validation and the ordering rule are **owned by `tinywasm/router`**
  and this package only applies them, with one sentence on why (two matchers
  drift; the conformance suite is what keeps httpd and the edge honest).
- **`README.md`** — if it shows route registration, add one `{id}` example and
  a line pointing at `tinywasm/router`'s `docs/INTROSPECTION.md` for mounting
  `/_routes` from a Worker.
- Do **not** link `docs/PLAN.md` from any permanent document.

## Acceptance criteria

- [ ] `go build ./...` and `go vet ./...` clean.
- [ ] `GOOS=js GOARCH=wasm go vet ./edge/...` clean.
- [ ] `gotest ./...` green, including the conformance suite with its new
      parameter clauses.
- [ ] `grep -rn "pathMatches" .` → empty.
- [ ] `grep -rn "map\[" edge/` → empty.
- [ ] `grep -rn "\"strings\"\|\"strconv\"\|\"errors\"\|\"reflect\"" edge/` →
      empty; the only `fmt` is `github.com/tinywasm/fmt`.
- [ ] `grep -rn "len(rt.info.Path) >" edge/` → empty (old tie-break removed).
- [ ] `grep -rn "RoutesEndpoint\|Introspection" edge/` → empty (`edge.Config`
      gained no field).
- [ ] `go.mod` contains **no** `replace` directive, and `router`/`model` are the
      published versions `go get @latest` resolved.
- [ ] `docs/ARCHITECTURE.md` records that matching is owned upstream.

## Out of scope

`{name...}` wildcards (rejected by `router.ValidatePattern` — do not add
support), any HTML, and any change to `d1/`, `r2/`, `workers/` or `files/`.

## Stages

| # | Stage | Files |
|---|---|---|
| 1 | `wasmContext.Param` | `edge/edge.go` |
| 2 | Delegate matching | `edge/edge.go` |
| 3 | Validate at registration | `edge/edge.go` |
| 4 | Wire values into the request | `edge/edge.go` |
| 5 | Tests | `tests/edge_params_test.go` |
| 6 | Documentation | `docs/ARCHITECTURE.md`, `README.md` |
