//go:build wasm

package workers

import (
	"syscall/js"
)

// Response is written by the handler and converted to a JS Response.
type Response struct {
	status  int
	headers map[string]string
	buf     []byte
}

func newResponse() *Response {
	return &Response{
		status:  200,
		headers: map[string]string{},
	}
}

// WriteHeader sets the HTTP status code.
func (w *Response) WriteHeader(code int) { w.status = code }

// Header returns the response headers map for setting values.
// Usage: w.Header()["Content-Type"] = "application/json"
func (w *Response) Header() map[string]string { return w.headers }

// Write appends bytes to the response body.
func (w *Response) Write(b []byte) (int, error) {
	w.buf = append(w.buf, b...)
	return len(b), nil
}

// WriteString appends a string to the response body.
func (w *Response) WriteString(s string) (int, error) {
	w.buf = append(w.buf, s...)
	return len(s), nil
}

// build converts the Go response to a JS Response object.
func (w *Response) build() js.Value {
	h := js.Global().Get("Headers").New()
	for k, v := range w.headers {
		h.Call("set", k, v)
	}

	init := js.Global().Get("Object").New()
	init.Set("status", w.status)
	init.Set("headers", h)

	// Binary-safe body transfer: copy bytes to a Uint8Array
	// rather than passing a string (which corrupts non-UTF8 data).
	b := w.buf
	ua := js.Global().Get("Uint8Array").New(len(b))
	js.CopyBytesToJS(ua, b)

	return js.Global().Get("Response").New(ua, init)
}
