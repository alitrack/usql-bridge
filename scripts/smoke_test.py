#!/usr/bin/env python3
"""Runtime smoke test for a freshly built usql-bridge c-shared library.

Loads the artifact with ctypes (the same C ABI LuaJIT FFI uses), opens a
throwaway SQLite database through the embedded xo/usql driver, and checks a
write/read round trip. Runs identically on Linux, macOS and Windows, so CI can
prove that each platform artifact actually loads and queries.

    python3 scripts/smoke_test.py dist/usqlbridge-linux-amd64.so
"""
import ctypes
import json
import os
import sys
import tempfile

MIN_BYTES = 1_000_000  # artifacts are ~10-15 MB; anything tiny is a broken build


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: smoke_test.py <path-to-bridge-library>", file=sys.stderr)
        return 2
    path = os.path.abspath(sys.argv[1])
    size = os.path.getsize(path)
    if size < MIN_BYTES:
        print(f"FAIL: {path} is only {size} bytes", file=sys.stderr)
        return 1

    lib = ctypes.CDLL(path)
    lib.usql_connect.restype = ctypes.c_char_p
    lib.usql_connect.argtypes = [ctypes.c_char_p]
    lib.usql_query.restype = ctypes.c_char_p
    lib.usql_query.argtypes = [ctypes.c_int, ctypes.c_char_p]
    lib.usql_exec.restype = ctypes.c_char_p
    lib.usql_exec.argtypes = [ctypes.c_int, ctypes.c_char_p]
    lib.usql_close.restype = ctypes.c_int
    lib.usql_close.argtypes = [ctypes.c_int]

    tmpdir = tempfile.mkdtemp(prefix="usqlbridge-smoke-")
    # dburl wants a URL-style path: moderncsqlite:///<abs-path-with-forward-slashes>
    dbpath = os.path.join(tmpdir, "smoke.db").replace("\\", "/").lstrip("/")
    dsn = f"moderncsqlite:///{dbpath}"

    conn = lib.usql_connect(dsn.encode())
    print("connect:", conn)
    if not conn.startswith(b"id="):
        print(f"FAIL: connect returned {conn!r}", file=sys.stderr)
        return 1
    cid = int(conn.split(b"=")[1])

    def exec_sql(sql: str) -> bytes:
        out = lib.usql_exec(cid, sql.encode())
        print("exec:", sql, "->", out)
        return out

    def query_sql(sql: str):
        raw = lib.usql_query(cid, sql.encode())
        if raw.startswith(b"ERR:"):
            raise RuntimeError(raw.decode())
        return json.loads(raw)

    try:
        if not exec_sql("CREATE TABLE t(a INTEGER, b TEXT)").startswith(b"OK"):
            return 1
        if not exec_sql("INSERT INTO t VALUES (1,'x'),(2,'y')").startswith(b"OK"):
            return 1
        rows = query_sql("SELECT a, b FROM t ORDER BY a")
        print("rows:", rows)
        if rows != [{"a": 1, "b": "x"}, {"a": 2, "b": "y"}]:
            print(f"FAIL: unexpected rows {rows}", file=sys.stderr)
            return 1
        agg = query_sql("SELECT count(*) AS n, sum(a) AS s FROM t")
        if agg != [{"n": 2, "s": 3}]:
            print(f"FAIL: unexpected aggregate {agg}", file=sys.stderr)
            return 1
        if lib.usql_close(cid) != 1:
            print("FAIL: close did not report success", file=sys.stderr)
            return 1
    finally:
        try:
            os.remove(os.path.join(tmpdir, "smoke.db"))
            os.rmdir(tmpdir)
        except OSError:
            pass

    print(f"SMOKE OK  {os.path.basename(path)}  ({size} bytes)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
