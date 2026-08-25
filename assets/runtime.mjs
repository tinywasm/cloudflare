import { connect } from "cloudflare:sockets";
import mod from "./worker.wasm";

export async function loadModule() {
  return mod;
}

// The context handed to the isolate's single Go instance. It carries only
// isolate-stable values. The per-request ExecutionContext is deliberately absent:
// with one instance serving many requests, a captured ExecutionContext would
// belong to whichever request happened to start the isolate and would be stale —
// and therefore unsafe — for every request after it.
export function createRuntimeContext({ env, binding }) {
  return {
    env,
    connect,
    binding,
  };
}
