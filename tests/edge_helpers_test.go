//go:build wasm

package cloudflare_test

import (
	"syscall/js"
	"testing"

	"github.com/tinywasm/model"
	"github.com/tinywasm/router"
)

// captureRoute records the access a handler was registered with.
type captureRoute struct {
	public   bool
	requires bool
	resource model.Resource
	action   model.Action
}

func (r *captureRoute) Requires(resource model.Resource, action model.Action) router.Route {
	r.requires = true
	r.resource = resource
	r.action = action
	return r
}
func (r *captureRoute) Authenticated() router.Route { return r }
func (r *captureRoute) Public() router.Route {
	r.public = true
	return r
}
func (r *captureRoute) Accepts(args model.Fielder) router.Route {
	return r
}

// captureRouter keeps the handlers instead of serving them, so a test can call ONE directly
// and unit-test what it does: magic-byte validation, key generation, the binary round trip.
//
// It does NOT go through the access gate, and that is a deliberate limit, not a shortcut —
// it is why it may never be the only thing testing this package. Trusting it for access
// control is how the upload shipped as a permanent 403 with a green suite. The gate is the
// job of TestEdgeConformance, which drives the real pipeline.
type captureRouter struct {
	get, put router.HandlerFunc
	getRoute *captureRoute
	putRoute *captureRoute
}

func (r *captureRouter) Get(path string, h router.HandlerFunc) router.Route {
	r.get = h
	r.getRoute = &captureRoute{}
	return r.getRoute
}
func (r *captureRouter) Put(path string, h router.HandlerFunc) router.Route {
	r.put = h
	r.putRoute = &captureRoute{}
	return r.putRoute
}
func (r *captureRouter) Post(path string, h router.HandlerFunc) router.Route {
	return &captureRoute{}
}
func (r *captureRouter) Delete(path string, h router.HandlerFunc) router.Route {
	return &captureRoute{}
}
func (r *captureRouter) Options(path string, h router.HandlerFunc) router.Route {
	return &captureRoute{}
}
func (r *captureRouter) Handle(method, path string, h router.HandlerFunc) router.Route {
	return &captureRoute{}
}
func (r *captureRouter) Stream(path string, h router.StreamFunc) router.Route {
	return &captureRoute{}
}
func (r *captureRouter) Socket(path string, h router.SocketFunc) router.Route {
	return &captureRoute{}
}
func (r *captureRouter) Op(id string) router.Route {
	return &captureRoute{}
}
func (r *captureRouter) PublicAsset(path string, h router.HandlerFunc) {}
func (r *captureRouter) PublicDir(prefix string, dir string)           {}
func (r *captureRouter) Use(m ...router.Middleware)                    {}
func (r *captureRouter) Routes() []router.RouteInfo                    { return nil }

// promise wraps a value in a resolved JS Promise — every Cloudflare binding is async.
func promise(v js.Value) js.Value {
	return js.Global().Get("Promise").Call("resolve", v)
}

// fakeBucket implements the shape of an R2 binding: put/get/delete returning Promises.
func fakeBucket(store map[string][]byte, contentTypes map[string]string) js.Value {
	b := js.Global().Get("Object").New()

	b.Set("put", js.FuncOf(func(_ js.Value, args []js.Value) any {
		key := args[0].String()
		ua := args[1]
		buf := make([]byte, ua.Get("byteLength").Int())
		js.CopyBytesToGo(buf, ua) // bytes in, verbatim
		store[key] = buf

		if len(args) > 2 {
			opts := args[2]
			httpMetadata := opts.Get("httpMetadata")
			if !httpMetadata.IsUndefined() && !httpMetadata.IsNull() {
				ct := httpMetadata.Get("contentType")
				if !ct.IsUndefined() && !ct.IsNull() {
					contentTypes[key] = ct.String()
				}
			}
		}
		return promise(js.Undefined())
	}))

	b.Set("get", js.FuncOf(func(_ js.Value, args []js.Value) any {
		key := args[0].String()
		data, ok := store[key]
		if !ok {
			return promise(js.Null()) // R2 returns null for a missing key
		}

		// R2 get returns an R2Object. For simplicity in tests, we'll return
		// an object that has a .body which is a ReadableStream (or something that Response can handle)
		// and .httpMetadata.

		obj := js.Global().Get("Object").New()

		ua := js.Global().Get("Uint8Array").New(len(data))
		js.CopyBytesToJS(ua, data)

		// In our R2 implementation, we use new Response(obj.body).arrayBuffer()
		// So obj.body can be a Uint8Array and Response will handle it.
		obj.Set("body", ua)

		httpMetadata := js.Global().Get("Object").New()
		if ct, ok := contentTypes[key]; ok {
			httpMetadata.Set("contentType", ct)
		}
		obj.Set("httpMetadata", httpMetadata)

		return promise(obj)
	}))

	b.Set("delete", js.FuncOf(func(_ js.Value, args []js.Value) any {
		key := args[0].String()
		delete(store, key)
		delete(contentTypes, key)
		return promise(js.Undefined())
	}))

	return b
}

func setupEnv(t *testing.T) map[string][]byte {
	store := map[string][]byte{}
	contentTypes := map[string]string{}
	env := js.Global().Get("Object").New()
	env.Set("FILES", fakeBucket(store, contentTypes))

	ctx := js.Global().Get("Object").New()
	ctx.Set("env", env)
	js.Global().Set("context", ctx) // exactly what Cloudflare injects

	t.Cleanup(func() { js.Global().Delete("context") })
	return store
}
