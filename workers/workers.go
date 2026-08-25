//go:build wasm

package workers

import (
	"syscall/js"

	"github.com/tinywasm/cloudflare/log"
)

// Handle registers fn as the single request handler and blocks forever.
// fn is called for every incoming HTTP request to the Worker.
// This must be called from main(); it never returns.
//
// Uses the binding pattern from goflare/assets/worker.mjs:
//
//	binding.handleRequest is called per request with the JS Request object.
//	binding is accessed via js.Global().Get("context") — injected by wasm_exec.js Proxy.
func Handle(fn func(*Response, *Request)) {
	// Access the runtime context injected by worker.mjs into go.run(instance, ctx).
	// wasm_exec.js patches global with a Proxy: global.context → ctx = {env, binding}.
	binding := js.Global().Get("context").Get("binding")

	binding.Set("handleRequest", js.FuncOf(func(this js.Value, args []js.Value) any {
		req := args[0]
		return newPromise(func() (res js.Value, err error) {
			method := req.Get("method").String()
			url := req.Get("url").String()

			r, err := newRequest(req)
			if err != nil {
				log.Fail(500, method, url, err)
				return errorResponse(500, "failed to parse request"), nil
			}
			w := newResponse()

			// The request boundary is the last place a panic can be caught. Past it the
			// wasm instance dies and Cloudflare answers 1101 "Worker threw exception" with
			// the cause nowhere to be found — and it takes every in-flight request with it.
			// res is named so the recovered path still answers, instead of resolving the
			// promise with an undefined Response.
			defer func() {
				if v := recover(); v != nil {
					log.Panic(method, url, v)
					res, err = errorResponse(500, "internal error"), nil
				}
			}()

			fn(w, r)
			return w.build(), nil
		})
	}))

	Ready()
	select {}
}

// Ready signals the Workers runtime that Go initialization is complete.
// Called automatically by Handle(). Call manually only if not using Handle().
//
// It reads the callback from context.binding — the same instance-scoped door
// Handle() uses — never from a bare global. js.Global() is a Proxy
// (assets/wasm_exec_worker.js) that resolves ONLY the "context" property per wasm
// instance; every other name resolves to the single object shared by the whole
// isolate, where a second request would silently clobber the signal. See
// assets/worker.mjs's start() for the JS side of this contract.
func Ready() {
	ready := js.Global().Get("context").Get("binding").Get("ready")
	if !ready.IsNull() && !ready.IsUndefined() {
		ready.Invoke()
	}
}

// newPromise wraps a blocking Go func in a JS Promise.
func newPromise(fn func() (js.Value, error)) js.Value {
	executor := js.FuncOf(func(this js.Value, args []js.Value) any {
		resolve, reject := args[0], args[1]
		go func() {
			result, err := fn()
			if err != nil {
				reject.Invoke(js.ValueOf(err.Error()))
				return
			}
			resolve.Invoke(result)
		}()
		return nil
	})
	return js.Global().Get("Promise").New(executor)
}

// errorResponse builds a minimal JS Response for internal errors.
func errorResponse(status int, msg string) js.Value {
	h := js.Global().Get("Headers").New()
	h.Call("set", "Content-Type", "text/plain")
	init := js.Global().Get("Object").New()
	init.Set("status", status)
	init.Set("headers", h)
	return js.Global().Get("Response").New(js.ValueOf(msg), init)
}
