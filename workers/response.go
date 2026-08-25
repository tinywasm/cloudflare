//go:build wasm

package workers

import (
	"syscall/js"
)

// Response is written by the handler and converted to a JS Response.
type Response struct {
	status  int
	headers js.Value // JS Headers object, built incrementally — no Go map, same approach as Request.headers
	buf     []byte
}

func newResponse() *Response {
	return &Response{
		status:  200,
		headers: js.Global().Get("Headers").New(),
	}
}

// WriteHeader sets the HTTP status code.
func (w *Response) WriteHeader(code int) { w.status = code }

// SetHeader sets a response header. Usage: w.SetHeader("Content-Type", "application/json")
func (w *Response) SetHeader(key, value string) {
	w.headers.Call("set", key, value)
}

// GetHeader returns a previously-set response header, or "" if absent.
func (w *Response) GetHeader(key string) string {
	v := w.headers.Call("get", key)
	if v.IsNull() || v.IsUndefined() {
		return ""
	}
	return v.String()
}

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
	init := js.Global().Get("Object").New()
	init.Set("status", w.status)
	init.Set("headers", w.headers)

	// Binary-safe body transfer: copy bytes to a Uint8Array
	// rather than passing a string (which corrupts non-UTF8 data).
	b := w.buf
	ua := js.Global().Get("Uint8Array").New(len(b))
	js.CopyBytesToJS(ua, b)

	return js.Global().Get("Response").New(ua, init)
}
