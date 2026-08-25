//go:build wasm

package cloudflare_test

import (
	"syscall/js"
	"testing"

	"github.com/tinywasm/cloudflare/workers"
)

// TestReady_SignalsItsOwnInstanceNotASharedGlobal is the regression test for the
// production hang that made veltylabs/iam answer every request with Cloudflare
// error 1101 on iam.velty.cl. The server-side log was not an exception:
//
//	The Workers runtime canceled this request because it detected that your
//	Worker's code had hung and would never generate a response.
//
// The cause is the Go→JS startup handshake. assets/worker.mjs hands each Go
// instance its own runtime context, and assets/wasm_exec_worker.js installs a
// Proxy over the real global so that js.Global().Get("context") — and ONLY that
// property name — resolves per instance:
//
//	get(target, prop) {
//	    if (prop === 'context') { return context; }   // per instance
//	    return Reflect.get(...arguments);             // the REAL, shared global
//	}
//
// Ready() reads js.Global().Get("workers"), which falls through to the shared
// global. worker.mjs assigns globalThis.workers on every invocation, so once two
// requests overlap — and they overlap the moment startup awaits anything, e.g. a
// D1 schema sync — the later request's assignment wins, and the earlier
// instance's Ready() resolves the WRONG promise. Its own `await readyPromise`
// then never resolves, no I/O is outstanding, and the runtime cancels it as hung.
//
// Ready() must therefore signal through the instance-scoped binding — the same
// door Handle() already uses for handleRequest — never through a shared global.
func TestReady_SignalsItsOwnInstanceNotASharedGlobal(t *testing.T) {
	saveHandshakeGlobals(t)

	// This instance's own context, exactly as worker.mjs builds it per Go
	// instance: {env, binding} reached through js.Global().Get("context").
	ownCalled := false
	own := js.FuncOf(func(js.Value, []js.Value) any {
		ownCalled = true
		return nil
	})
	defer own.Release()

	binding := js.Global().Get("Object").New()
	binding.Set("ready", own)
	ctx := js.Global().Get("Object").New()
	ctx.Set("binding", binding)
	js.Global().Set("context", ctx)

	// A second request started while this instance was still initializing and
	// overwrote the shared slot — the exact assignment worker.mjs performs on
	// every invocation.
	otherCalled := false
	other := js.FuncOf(func(js.Value, []js.Value) any {
		otherCalled = true
		return nil
	})
	defer other.Release()

	shared := js.Global().Get("Object").New()
	shared.Set("ready", other)
	js.Global().Set("workers", shared)

	workers.Ready()

	if otherCalled {
		t.Error("Ready() signalled through the shared globalThis.workers slot: it resolved a " +
			"concurrent request's promise instead of its own, leaving this instance's request " +
			"hung until the Workers runtime cancels it")
	}
	if !ownCalled {
		t.Error("Ready() did not signal its own instance's binding (context.binding.ready): " +
			"this instance's request never resolves")
	}
}

// saveHandshakeGlobals restores whatever the test environment had once the test
// ends, so this test cannot leak the fixture into the rest of the suite.
func saveHandshakeGlobals(t *testing.T) {
	t.Helper()
	prevContext := js.Global().Get("context")
	prevWorkers := js.Global().Get("workers")
	t.Cleanup(func() {
		if prevContext.IsUndefined() {
			js.Global().Delete("context")
		} else {
			js.Global().Set("context", prevContext)
		}
		if prevWorkers.IsUndefined() {
			js.Global().Delete("workers")
		} else {
			js.Global().Set("workers", prevWorkers)
		}
	})
}
