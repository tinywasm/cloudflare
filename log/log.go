//go:build wasm

// Package log is the minimum an edge app must report to be operable.
//
// The rule it enforces: no response of 400 or worse leaves the Worker without a line
// saying why. A bare status code in a dashboard tells the operator that something broke,
// never what — and at the edge there is no shell to go and look.
//
// It is deliberately small. Successful requests are NOT logged: Cloudflare already records
// every request with its method, path and status, so an access log here would only
// duplicate it, cost CPU on the hot path and bury the lines that matter.
//
// Never pass a request body, a header or a cookie to these functions. Logs are read by
// people and shipped to third parties; a body can carry credentials or personal data.
package log

import (
	"syscall/js"

	"github.com/tinywasm/fmt"
)

const prefix = "goflare"

// Reject records a request refused on purpose: a 4xx. The client got it wrong (too big,
// wrong type, no permission), so this is not an incident — but without the reason, a 403 or
// a 415 is indistinguishable from a bug in the app.
func Reject(status int, method, path, reason string) {
	console("warn", fmt.Convert(status).String(), method, path, reason)
}

// Fail records a failure that is ours: a 5xx. err is the cause, and it is the whole point
// of the call — a 502 with no cause cannot be acted on.
func Fail(status int, method, path string, err error) {
	msg := "unknown cause"
	if err != nil {
		msg = err.Error()
	}
	console("error", fmt.Convert(status).String(), method, path, msg)
}

// Panic records a panic that was caught at the request boundary.
//
// This is the line that must never be missing. An unrecovered panic tears down the wasm
// instance and Cloudflare answers 1101 "Worker threw exception" — a generic error, with the
// real cause nowhere to be seen. Recovering and logging turns that dead end into a 500 that
// names what happened.
func Panic(method, path string, v any) {
	console("error", "500", method, path, "panic: "+fmt.Convert(v).String())
}

func console(level string, status, method, path, detail string) {
	js.Global().Get("console").Call(level,
		prefix+" "+status+" "+method+" "+path+" — "+detail)
}
