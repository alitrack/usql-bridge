# usql-bridge

In-process **Go c-shared** bridge that embeds [xo/usql](https://github.com/xo/usql)'s
`database/sql` drivers behind a LuaJIT FFI table function, so DuckDB (via the
[duckdb-luajit](https://github.com/alitrack/duckdb-luajit) extension) can query
long-tail databases **with a resident connection and no per-query process start**.

Chinese version: [README_cn.md](README_cn.md)

## Why

The existing `dbcli` route shells out to a `usql` binary for every query:
30–100 ms of process cold start each time. This bridge keeps the connection
inside the DuckDB process and hands the same connection back for every query:

| Route | Cost per query | Needs |
|---|---|---|
| `dbcli` + `usql` binary | 30–100 ms (process spawn) | `usql` installed on the host |
| **usql-bridge** (this repo) | **~0.1–0.2 ms sustained** | one c-shared artifact, no external binary |

The mechanism is one line of design: **every usql driver *is* a standard
`database/sql` driver**, and `drivers.Open(ctx, *dburl.URL, nil, nil)` returns a
`*sql.DB` that stays open. The bridge never touches usql's CLI/REPL internals.

Scope note: for mainstream analytical sources, `duckdb_universal` (native Rust
connectors) remains the better answer. This bridge fills the middle gap — a
long tail of rarely-queried databases where a global binary isn't installed but
an in-process connection is worth having.

## Artifacts

| Platform | File |
|---|---|
| Linux x86_64 | `usqlbridge-linux-amd64.so` |
| Linux arm64 | `usqlbridge-linux-arm64.so` |
| macOS Apple Silicon | `usqlbridge-darwin-arm64.dylib` |
| macOS Intel | `usqlbridge-darwin-amd64.dylib` |
| Windows x86_64 | `usqlbridge-windows-amd64.dll` |

## Files

| File | Purpose |
|---|---|
| `main.go` | the bridge: `//export usql_connect / usql_query / usql_exec / usql_close` |
| `usql.lua` | LuaJIT FFI wrapper — `ffi.cdef` + `ffi.load`, returns JSON strings |
| `build-release.sh` | builds c-shared artifacts per target (`linux-amd64`, `linux-arm64`, `darwin-arm64`, `darwin-amd64`, `windows-amd64`) |
| `scripts/smoke_test.py` | loads a built artifact with ctypes and runs a SQL round trip — the per-platform CI gate |
| `test_usql_bridge.sql` | full chain through DuckDB → luajit → bridge → SQLite, plus a 200-query benchmark |
| `.github/workflows/` | `ci.yml` (build + smoke test on every push) and `release.yml` (tag → all-platform release) |
| `usqlbridge.h` | generated cgo header, kept in-tree for reference |

## Usage

```sql
LOAD 'luajit.duckdb_extension';

SELECT * FROM luajit_module(mode := 'quick_compile', sql_name := 'usql',
  source := 'return dofile(''usql.lua'')');

-- connect (cold start happens here: the bridge Pings immediately)
SELECT luajit_s('usql', {op: 'connect', url: 'moderncsqlite:///tmp/app.db'});

-- query: one JSON object per row
SELECT luajit_s('usql', {op: 'query', id: 1, sql: 'SELECT id, name FROM src ORDER BY id'});

-- write
SELECT luajit_s('usql', {op: 'exec', id: 1, sql: 'INSERT INTO src VALUES (9, ''zeta'')'});

-- sustained-query benchmark
SELECT luajit_s('usql', {op: 'benchmark', id: 1, n: 200, sql: 'SELECT 1'});

SELECT luajit_s('usql', {op: 'close', id: 1});
```

Connection ids stay valid for the lifetime of the DuckDB process (Go side keeps
`map[int]*sql.DB`). Errors come back as a single `ERR: <reason>` row.

## Drivers (schemes)

The release embeds **`moderncsqlite`** — pure-Go SQLite, **no CGO at runtime**.
The scheme is `moderncsqlite`, *not* `sqlite3`: those are different usql drivers
and the wrong one simply fails to connect.

Every usql driver registers itself in `init()`, so adding a backend is one
import plus a rebuild:

```go
_ "github.com/xo/usql/drivers/postgres"   // then: postgres://user@host/db
```

DSN syntax follows `github.com/xo/dburl` — the same URLs the `usql` CLI takes.

## Build from source

```bash
./build-release.sh                  # every target this host can build (others are skipped)
./build-release.sh linux-amd64      # one target
python3 scripts/smoke_test.py dist/usqlbridge-linux-amd64.so
```

Requirements: Go ≥ 1.26.1 (usql's floor), cgo enabled, and a C toolchain for
the target (`gcc`, `gcc-aarch64-linux-gnu`, `x86_64-w64-mingw32-gcc`, or clang
on macOS).

**cgo is mandatory.** `main.go` has `import "C"` and uses `//export`, so
`-buildmode=c-shared` needs `CGO_ENABLED=1`; with `CGO_ENABLED=0` the go tool
reports *"build constraints exclude all Go files"*. Because darwin needs a
Mach-O linker and the Apple SDK, macOS artifacts are built on a macOS runner —
there is no usable darwin cross-toolchain on Linux. That is why releases come
from a native runner matrix (`.github/workflows/release.yml`) instead of one
machine cross-compiling everything.

## Measured

Environment: go1.26.1, DuckDB 1.5.5, luajit ELF extension, SQLite via
`moderncsqlite`.

| Item | Result |
|---|---|
| connect + Ping | `id=1`, cold start absorbed at connect |
| CREATE / INSERT / SELECT / aggregate / write-read-back / close | all correct |
| single query (incl. FFI hop + JSON encoding) | 0.1–0.3 ms |
| 200 sustained queries | ~0.2 ms each, no cold start, no degradation |

## Pitfalls hit while building this

1. **`C.CString` must be freed Lua-side** — and `free` has to be declared in
   `ffi.cdef` (`missing declaration for symbol 'free'` otherwise).
2. **`ffi.cdef` must match the `//export` signature exactly.** Changing
   `usql_connect` from `C.int` to `*C.char` (to carry errors) without updating
   the cdef produced `cannot convert 'number' to 'const char *'` — the connect
   had actually succeeded; only the presentation layer was wrong.
3. **`local ok = pcall(ffi.load, p)` only captures a boolean** — the library
   object is the *second* return value.
4. **Lua closures only see `local`s declared before them**; a later `local`
   reads as a global and evaluates to `nil` at call time.
5. **usql's SQLite scheme is `moderncsqlite`**, not `sqlite3`.
6. **A DuckDB `COPY ... (FORMAT CSV)` file is not a SQLite database** even when
   named `.db`; let the driver `CREATE TABLE` its own fixture instead.
7. **GitHub *release* download CDN is not the raw CDN.** They fail
   independently — a `.so` pulled from a release can stall where
   `raw.githubusercontent.com` is instant, so downloads stay best-effort and
   the library prints an actionable error telling you where to drop the file.
8. **`/mnt/d` is a slow 9p mount** — keep `GOMODCACHE`/`GOPATH` on the native
   filesystem.

## Status

v0.1.1 — all five platform artifacts built and smoke-tested in CI. Only the
SQLite (`moderncsqlite`) driver has been exercised end-to-end so far; the other
schemes are one import away but not yet verified against live servers.

## License

MIT — see [LICENSE](LICENSE).
