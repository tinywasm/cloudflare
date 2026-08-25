import "./wasm_exec.js";
import { createRuntimeContext, loadModule } from "./runtime.mjs";

// The startup promise for this isolate's single Go instance. This variable IS the
// concurrency contract: ensureStarted assigns it synchronously, before any await,
// so N concurrent requests all await the SAME startup instead of each building
// their own instance and racing to signal it.
let started;

// The one object the Go instance registers its handlers on (handleRequest, and
// the ready signal below). Module scope, not per request: the instance that
// registers handleRequest outlives every individual request.
const binding = {};

globalThis.tryCatch = (fn) => {
  try {
    return {
      result: fn(),
    };
  } catch (e) {
    return {
      error: e,
    };
  }
};

// start boots the Go instance exactly once. `env` is captured here because in
// Workers the bindings object is identical for every request in an isolate.
// Anything that genuinely varies per request must travel through
// binding.handleRequest instead — never through this context.
async function start(env) {
  const wasmModule = await loadModule();
  const go = new Go();

  let ready;
  const readyPromise = new Promise((resolve) => {
    ready = resolve;
  });
  // Go signals init completion via
  // js.Global().Get("context").Get("binding").Get("ready") — see workers.Ready.
  // This MUST ride ctx.binding and never globalThis: wasm_exec_worker.js gives
  // each instance a Proxy that resolves ONLY the "context" property per instance,
  // so any other global name is the single object shared by the whole isolate.
  binding.ready = () => {
    ready();
  };

  const instance = new WebAssembly.Instance(wasmModule, go.importObject);

  // go.run settles when Go's main() returns. In a healthy Worker that never
  // happens — Handle() blocks forever once it has registered handleRequest — so
  // settling here means main() returned WITHOUT signalling readiness. Since
  // `started` is cached for the life of the isolate, readyPromise would then hang
  // EVERY request from now on, with nothing logged: exactly the failure that is
  // reported as "the Workers runtime canceled this request because it detected
  // that your Worker's code had hung". Unblocking and throwing turns that silent,
  // permanent hang into one named error, and every later request fails fast on
  // the same cached rejection instead of timing out.
  let exitedEarly = false;
  let exitError;
  const settleOnExit = (err) => {
    exitedEarly = true;
    exitError = err;
    ready();
  };
  go.run(instance, createRuntimeContext({ env, binding })).then(
    () => settleOnExit(undefined),
    settleOnExit
  );

  await readyPromise;
  if (exitedEarly) {
    // A rejection carries the real failure — a wasm trap during package
    // initialization reaches us here and NOWHERE else, because it aborts before
    // Go can print anything of its own. Rethrow it untouched; replacing it with
    // a generic message is what makes such a crash undiagnosable.
    if (exitError !== undefined) {
      throw exitError;
    }
    throw new Error(
      "goflare: Go main() returned without registering a request handler — " +
        "check the Worker's logs for the error it printed before returning"
    );
  }
}

// ensureStarted is deliberately NOT async: the check-and-assign must complete
// without yielding to the event loop, or two concurrent requests could both see
// `started === undefined` and start two instances.
function ensureStarted(env) {
  if (started === undefined) {
    started = start(env);
  }
  return started;
}

async function fetch(req, env, ctx) {
  await ensureStarted(env);
  const res = await binding.handleRequest(req);
  const out = new Response(res.body, res);
  out.headers.set("x-goflare", "__GOFLARE_VERSION__");
  return out;
}

async function scheduled(event, env, ctx) {
  await ensureStarted(env);
  return binding.runScheduler(event);
}

async function queue(batch, env, ctx) {
  await ensureStarted(env);
  return binding.handleQueueMessageBatch(batch);
}

export default {
  fetch,
  scheduled,
  queue,
};
