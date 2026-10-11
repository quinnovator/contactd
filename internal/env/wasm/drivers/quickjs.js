// contactd tier-0 driver for QuickJS on WASI. Run as: qjs --std -e <this file>.
//
// Reads requests from stdin and evaluates each as a script in the one global
// scope, so variables and functions survive from one request to the next:
//
//   stdin   <token> SP <n> LF, then n bytes of UTF-8 source
//   stdout  whatever the code prints, then NUL <token> ':' <ddd> LF
//
// ddd is the exit status: 0, or 1 for an uncaught exception. The value of the
// last expression is echoed unless it is undefined, as a JS console would.
//
// Scripts are evaluated the way the qjs REPL does it: top-level await works,
// and a request is finished when its promise settles. Each request is read
// from inside a promise job, so between requests control returns to the
// event loop and timers and pending promises get to run.
(function () {
  const std = globalThis.std;
  const input = std.in;
  const evalScript = std.evalScript;

  function show(value) {
    if (typeof value === "string") return JSON.stringify(value);
    if (typeof value === "object" && value !== null) {
      try {
        const json = JSON.stringify(value);
        if (json !== undefined) return json;
      } catch (_) {
        // cyclic or otherwise not JSON; fall through
      }
    }
    return String(value);
  }

  function report(error) {
    let text = String(error);
    if (error instanceof Error && error.stack) text += "\n" + error.stack.trimEnd();
    std.err.puts(text + "\n");
  }

  function finish(token, status) {
    std.out.flush();
    std.err.flush();
    std.out.puts("\x00" + token + ":" + String(status).padStart(3, "0") + "\n");
    std.out.flush();
    serve();
  }

  function serve() {
    const header = input.getline();
    if (header === null) return;
    const space = header.indexOf(" ");
    const token = header.slice(0, space);
    const size = parseInt(header.slice(space + 1), 10);
    const source = size > 0 ? input.readAsString(size) : "";
    let result;
    try {
      // backtrace_barrier keeps this driver's frames out of stack traces.
      result = evalScript(source, { async: true, backtrace_barrier: true });
    } catch (error) {
      report(error);
      finish(token, 1);
      return;
    }
    result.then(
      (settled) => {
        if (settled.value !== undefined) std.out.puts(show(settled.value) + "\n");
        finish(token, 0);
      },
      (error) => {
        report(error);
        finish(token, 1);
      },
    );
  }

  serve();
})();
