//go:build !wasm

package cloudflare_test

import (
	"os"
	"strings"
	"testing"
)

// wasmExecWorker is cloudflare's vendored copy of TinyGo's wasm_exec.js. It is
// embedded by assets/embed.go and bundled verbatim into every deployed edge.js,
// which makes it a hard ABI contract with whatever TinyGo version compiled
// edge.wasm.
const wasmExecWorker = "../assets/wasm_exec_worker.js"

// TestWasmExec_TicksUsesInt64ABI is the regression test for the crash that made
// veltylabs/iam unreachable in production. The symptom reported by Cloudflare was
// only "the Workers runtime canceled this request because it detected that your
// Worker's code had hung"; the actual failure, recovered by running the same
// artifacts under workerd, was:
//
//	TypeError: Cannot convert 1787642221321 to a BigInt
//
// That number is `timeOrigin + performance.now()` — a millisecond timestamp.
// TinyGo declares the runtime.ticks import as returning **i64 nanoseconds**, so
// the JS side must return a BigInt. The vendored copy had been taken from an
// older TinyGo where ticks was `float64` milliseconds, and returning a plain
// Number traps the module the first time the runtime reads the clock.
//
// The trap fires during package initialization, before main() runs, so the Worker
// prints nothing at all — which is exactly why it presented as an unexplained
// hang. Any Worker touching time even transitively (unixid, jwt, a time.Now() in
// an init) is affected; a Worker that never reads the clock is not, which is why
// the failure looked app-specific rather than like the ABI mismatch it was.
func TestWasmExec_TicksUsesInt64ABI(t *testing.T) {
	src, err := os.ReadFile(wasmExecWorker)
	if err != nil {
		t.Fatalf("read %s: %v", wasmExecWorker, err)
	}
	js := string(src)

	ticks, ok := importBody(js, `"runtime.ticks"`)
	if !ok {
		t.Fatal("could not locate the runtime.ticks import in wasm_exec_worker.js")
	}
	if !strings.Contains(ticks, "BigInt(") {
		t.Error("runtime.ticks returns a Number: TinyGo declares this import as i64, so the " +
			"module traps with \"Cannot convert <n> to a BigInt\" the first time the runtime " +
			"reads the clock — during package init, before main() can report anything")
	}
	if !strings.Contains(ticks, "1e6") {
		t.Error("runtime.ticks does not scale to nanoseconds: TinyGo expects int64 nanoseconds, " +
			"not the milliseconds performance.now() reports")
	}

	sleep, ok := importBody(js, `"runtime.sleepTicks"`)
	if !ok {
		t.Fatal("could not locate the runtime.sleepTicks import in wasm_exec_worker.js")
	}
	// The counterpart: TinyGo passes int64 nanoseconds in, so the value arrives as
	// a BigInt and setTimeout needs a Number of milliseconds.
	if !strings.Contains(sleep, "Number(timeout)") {
		t.Error("runtime.sleepTicks passes its argument straight to setTimeout: TinyGo hands it " +
			"an i64, so the value is a BigInt and must be converted before use")
	}
}

// importBody returns the source of the arrow function declared for the given
// import key, from its "=> {" to the matching close brace.
func importBody(src, key string) (string, bool) {
	i := strings.Index(src, key)
	if i < 0 {
		return "", false
	}
	arrow := strings.Index(src[i:], "=>")
	if arrow < 0 {
		return "", false
	}
	open := strings.Index(src[i+arrow:], "{")
	if open < 0 {
		return "", false
	}
	open += i + arrow

	depth := 0
	for j := open; j < len(src); j++ {
		switch src[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[open+1 : j], true
			}
		}
	}
	return "", false
}
