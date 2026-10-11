# contactd tier-0 driver for CPython on WASI. Run as: python -B -u -c <this file>.
#
# Reads requests from stdin and runs each in one long-lived namespace, so
# variables, imports and definitions survive from one request to the next:
#
#   stdin   <token> SP <n> LF, then n bytes of UTF-8 source
#   stdout  whatever the code prints, then NUL <token> ':' <ddd> LF
#
# ddd is the exit status: 0, 1 for an uncaught exception, or the argument of
# sys.exit(). The last statement of a request is echoed if it is an expression
# with a value, as the interactive interpreter would.
#
# Only modules that are built in or frozen are imported up front. Anything
# else is compiled from source on every start, and start time is the point.


def _contactd_serve():
    import _ast
    import io
    import os
    import sys

    stdin = sys.stdin.buffer
    # Code that reads stdin gets EOF, not the next request.
    sys.stdin = io.StringIO()

    main = type(sys)("__main__")
    sys.modules["__main__"] = main
    scope = main.__dict__
    sources = {}

    def run(source, name):
        try:
            tree = compile(source, name, "exec", _ast.PyCF_ONLY_AST)
            echo = None
            if tree.body and isinstance(tree.body[-1], _ast.Expr):
                echo = _ast.Expression(tree.body.pop().value)
            exec(compile(tree, name, "exec"), scope)
            if echo is not None:
                value = eval(compile(echo, name, "eval"), scope)
                if value is not None:
                    print(repr(value))
            return 0
        except SystemExit as stop:
            if stop.code is None:
                return 0
            if isinstance(stop.code, int):
                return stop.code & 0xFF
            print(stop.code, file=sys.stderr)
            return 1
        except BaseException as error:
            report(error)
            return 1

    def report(error):
        import linecache
        import traceback

        # Let the traceback quote source lines from this and earlier requests.
        for name, source in sources.items():
            linecache.cache[name] = (len(source), None, source.splitlines(True), name)
        # Drop this driver's own frame from the top.
        tb = error.__traceback__
        traceback.print_exception(type(error), error, tb.tb_next if tb else None)

    def flush():
        for stream in (sys.stdout, sys.stderr, sys.__stdout__, sys.__stderr__):
            try:
                stream.flush()
            except Exception:
                pass

    count = 0
    while True:
        header = stdin.readline()
        if not header:
            return
        token, size = header.split()
        source = stdin.read(int(size)).decode("utf-8", "replace")
        count += 1
        name = "<run %d>" % count
        sources[name] = source
        status = run(source, name)
        flush()
        os.write(1, b"\x00" + token + b":%03d\n" % status)


_contactd_serve()
