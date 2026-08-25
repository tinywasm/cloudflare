//go:build !wasm

// Package assets embeds the JS half of the Cloudflare Workers runtime, for
// goflare (or any other bundler) to read without vendoring a copy. The Go
// side of this contract lives in workers/ and edge/ — see AGENTS.md.
package assets

import _ "embed"

//go:embed wasm_exec_worker.js
var WasmExecJS []byte

//go:embed runtime.mjs
var RuntimeMJS []byte

//go:embed worker.mjs
var WorkerMJS []byte
