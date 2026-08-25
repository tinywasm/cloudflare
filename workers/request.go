//go:build wasm

package workers

import (
	"syscall/js"

	"github.com/tinywasm/fmt"
)

// Request represents an incoming HTTP request to the Worker.
type Request struct {
	Method  string
	URL     string
	jsReq   js.Value
	headers js.Value
	body    []byte
	hasBody bool
}

// Header returns the value of the named header, or "" if absent. Lookup is
// case-insensitive, per the Fetch Headers.get() contract — do not "fix" this
// by lowercasing key yourself; Headers.get() already does the right thing,
// and double-normalizing invites the exact bug this method replaces (see
// docs/PLAN.md at the time of this change: Headers.entries() lowercases
// names for iteration, but .get() matches case-insensitively — reading one
// as if it behaved like the other silently returned "" for every request).
func (r *Request) Header(key string) string {
	if r.headers.IsNull() || r.headers.IsUndefined() {
		return ""
	}
	v := r.headers.Call("get", key)
	if v.IsNull() || v.IsUndefined() {
		return ""
	}
	return v.String()
}

// Body returns the raw request body bytes.
// It reads the body lazily on the first call.
func (r *Request) Body() []byte {
	if r.hasBody {
		return r.body
	}

	ch := make(chan []byte, 1)
	errCh := make(chan string, 1)

	var thenFn, catchFn js.Func

	thenFn = js.FuncOf(func(this js.Value, args []js.Value) any {
		defer thenFn.Release()
		defer catchFn.Release()

		arrayBuffer := args[0]
		byteLength := arrayBuffer.Get("byteLength").Int()
		buf := make([]byte, byteLength)
		ua := js.Global().Get("Uint8Array").New(arrayBuffer)
		js.CopyBytesToGo(buf, ua)
		ch <- buf
		return nil
	})
	catchFn = js.FuncOf(func(this js.Value, args []js.Value) any {
		defer thenFn.Release()
		defer catchFn.Release()
		errCh <- args[0].String()
		return nil
	})

	r.jsReq.Call("arrayBuffer").Call("then", thenFn).Call("catch", catchFn)

	select {
	case b := <-ch:
		r.body = b
		r.hasBody = true
	case msg := <-errCh:
		// We could panic or just return nil, but workers usually don't expect
		// an error from Body() since it's not in the signature.
		// For now we write to stderr and return empty.
		js.Global().Get("console").Call("error", fmt.Errf("workers: failed to read body: %s", msg).Error())
		r.body = []byte{}
		r.hasBody = true
	}

	return r.body
}

// newRequest reads a JS Fetch Request into a Go Request.
func newRequest(jsReq js.Value) (*Request, error) {
	return &Request{
		Method:  jsReq.Get("method").String(),
		URL:     jsReq.Get("url").String(),
		jsReq:   jsReq,
		headers: jsReq.Get("headers"),
	}, nil
}
