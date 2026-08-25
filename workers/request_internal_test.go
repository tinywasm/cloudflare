//go:build wasm

package workers

import (
	"syscall/js"
	"testing"
)

// TestRequest_HeaderIsCaseInsensitive is the regression test for the bug this
// plan's own investigation found: newRequest used to build a map from
// Headers.entries(), which the Fetch spec lowercases, while callers looked
// keys up with their original case — so Cookie() returned false on every
// single request, in every deployed goflare Worker. Verified against the
// real Fetch API under workerd before this fix:
//
//	entries() key: cookie
//	get("Cookie"): sid=abc123
//	get("cookie"): sid=abc123
func TestRequest_HeaderIsCaseInsensitive(t *testing.T) {
	h := js.Global().Get("Headers").New()
	h.Call("set", "Cookie", "sid=abc123")

	jsReq := js.Global().Get("Object").New()
	jsReq.Set("method", "GET")
	jsReq.Set("url", "https://example.test/")
	jsReq.Set("headers", h)

	r, err := newRequest(jsReq)
	if err != nil {
		t.Fatalf("newRequest: %v", err)
	}

	for _, key := range []string{"Cookie", "cookie", "COOKIE"} {
		if got := r.Header(key); got != "sid=abc123" {
			t.Errorf("Header(%q) = %q, want %q", key, got, "sid=abc123")
		}
	}
	if got := r.Header("X-Missing"); got != "" {
		t.Errorf(`Header("X-Missing") = %q, want ""`, got)
	}
}
