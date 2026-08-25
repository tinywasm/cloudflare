//go:build wasm

package cloudflare_test

import (
	"testing"

	"github.com/tinywasm/cloudflare/edge"
	"github.com/tinywasm/router"
)

// TestMiddleware_WrappedOnceNotPerRequest is the regression test for
// gateAndServe rebuilding the middleware chain on every request. wraps counts
// how many times THIS middleware's own (HandlerFunc) HandlerFunc conversion
// runs — the composition step edge.go used to repeat per request — not how
// many times the resulting handler serves a request.
func TestMiddleware_WrappedOnceNotPerRequest(t *testing.T) {
	wraps := 0
	counting := func(next router.HandlerFunc) router.HandlerFunc {
		wraps++
		return next
	}

	r := edge.NewRouter(edge.Config{})
	r.Use(counting)
	r.Get("/ping", func(ctx router.Context) {
		ctx.Write([]byte("pong"))
	}).Public()

	edge.Validate(r)
	edge.ExportCompile(r)

	for i := 0; i < 3; i++ {
		edge.Dispatch(r, &conformanceCtx{method: "GET", path: "/ping"})
	}

	if wraps != 1 {
		t.Errorf("middleware composed %d times across 3 requests, want 1 — "+
			"gateAndServe is rebuilding the chain per request instead of reusing "+
			"a handler compiled once", wraps)
	}
}
