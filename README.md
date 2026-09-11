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
| `export.go` | native columnar export: `//export usql_export` — declared types → Parquet, no JSON |
| `usql.lua` | LuaJIT FFI wrapper — `ffi.cdef` + `ffi.load`, returns JSON strings / file paths |
| `build-release.sh` | builds c-shared artifacts per target (`linux-amd64`, `linux-arm64`, `darwin-arm64`, `darwin-amd64`, `windows-amd64`) |
| `scripts/smoke_test.py` | loads a built artifact with ctypes and runs a SQL round trip — the per-platform CI gate |
| `test_usql_bridge.sql` | full chain through DuckDB → luajit → bridge → SQLite, plus a 200-query benchmark |
| `test_export.sql` | native Parquet export end to end: types, bytes, no `from_json` on the DuckDB side |
| `.github/workflows/` | `ci.yml` (build + smoke test on every push) and `release.yml` (tag → all-platform release) |
| `usqlbridge.h` | generated cgo header, kept in-tree for reference |

## Usage

```sql
LOAD 'luajit.duckdb_extension';

SELECT * FROM luajit_module(mode := 'quick_compile', sql_name := 'usql',
  source := 'return dofile(''usql.lua'')');

-- the lib is a table function: one row per JSON object returned by the bridge
SELECT val FROM luajit_table('usql', list := '{"op":"connect","url":"moderncsqlite:////tmp/app.db"}');
SELECT val FROM luajit_table('usql', list := '{"op":"query","id":1,"sql":"SELECT id, name FROM src ORDER BY id"}');
SELECT val FROM luajit_table('usql', list := '{"op":"exec","id":1,"sql":"INSERT INTO src VALUES (9, ''zeta'')"}');
SELECT val FROM luajit_table('usql', list := '{"op":"export","id":1,"sql":"SELECT * FROM src","format":"parquet"}');
SELECT val FROM luajit_table('usql', list := '{"op":"benchmark","id":1,"n":200,"sql":"SELECT 1"}');
SELECT val FROM luajit_table('usql', list := '{"op":"close","id":1}');
```

(A convenience macro is also generated, e.g. `SELECT * FROM usql('{"op":"connect",...}')`
after `quick_compile`.)

Connection ids stay valid for the lifetime of the DuckDB process (Go side keeps
`map[int]*sql.DB`). Errors come back as a single `ERR: <reason>` row.

## Columnar export — `export` (v0.2.0)

The JSON ops are a *display* channel: JSON has only number/string/bool/null, so
DuckDB has to infer types back (DATE and DATETIME arrive as VARCHAR, BOOLEAN as
an integer, DECIMAL as DOUBLE) and text conversion turns non-UTF-8 bytes
(BLOBs) into U+FFFD irreversibly. `export` skips all of that: the bridge builds
a Parquet schema from the driver's **declared column types**, writes the values
as they come, and returns the file path.

```sql
-- table-function form
SELECT val FROM luajit_table('usql', list :=
  '{"op":"export","id":1,"sql":"SELECT * FROM src","path":"/tmp/src.parquet","format":"parquet"}');

-- scalar form: feed read_parquet directly
SELECT * FROM read_parquet(usql('{"op":"export","id":1,"sql":"SELECT * FROM src"}'));
```

* `path` — omitted ⇒ written to the system temp dir as
  `usql-export-<pid>-<nanos>.parquet` and **deleted when the connection is
  closed**. Give an explicit `path` and the file is yours to manage.
* `compression` — `snappy` (default) or `none`/`uncompressed`.
* `row_group_rows` — optional row-group size.
* Declared types are mapped straight through: INTEGER/BIGINT → `BIGINT`,
  REAL/DOUBLE → `DOUBLE`, TEXT/VARCHAR → `VARCHAR`, BLOB/BYTEA → `BLOB`,
  BOOLEAN → `BOOLEAN`, DATE → `DATE`, DATETIME/TIMESTAMP → `TIMESTAMP`
  (with/without time zone per the declared type). Unknown or all-NULL columns
  fall back to `VARCHAR` — the bridge never guesses a numeric type for an
  untyped column, because a wrong guess is silent wrong data.
* Known limit: `DECIMAL/NUMERIC` currently map to `DOUBLE` (precision beyond
  float64's ~15 digits is not preserved). Money-grade decimals need the
  `decimal(scale,precision)` path, which is not wired up yet.
* Cost: the binary grows from 10.7 MB to 20.8 MB (parquet-go + snappy codec).

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

## Getting the artifact (and verifying it)

`usql.lua` resolves the library as: `spec.lib` → `USQL_BRIDGE_LIB` →
`~/.duckdb/luajit-libs/usqlbridge-<os>-<arch>.<ext>` → one download attempt from
the v0.2.0 release. A download is accepted only if it clears a size floor **and**
carries the platform's magic bytes (ELF / PE / Mach-O): the release CDN does
truncate silently (measured: 7.6 MB of an 11.2 MB artifact after 6m25s, then
0 bytes on retry, while `raw.githubusercontent.com` answered in 1s).

Where the release CDN is slow or blocked, point the download at any mirror
prefix (trailing slash required) and let the library fetch it:

```bash
export USQL_BRIDGE_BASE_URL='https://gh-proxy.com/https://github.com/alitrack/usql-bridge/releases/download/v0.2.0/'
```

Or fetch it yourself and check the published checksums:

```bash
gh release download v0.2.0 --repo alitrack/usql-bridge
sha256sum -c SHA256SUMS            # 20.8 MB artifact verified this way
cp usqlbridge-linux-amd64.so ~/.duckdb/luajit-libs/
```

## Measured

Environment: go1.26.1, DuckDB 1.5.5, luajit ELF extension, SQLite via
`moderncsqlite`.

| Item | Result |
|---|---|
| connect + Ping | `id=1`, cold start absorbed at connect |
| CREATE / INSERT / SELECT / aggregate / write-read-back / close | all correct |
| single query (incl. FFI hop + JSON encoding) | 0.1–0.3 ms |
| 200 sustained queries | ~0.2 ms each, no cold start, no degradation |
| 100k rows × 10 declared types — `op=query` (JSON) | 0.70–0.76 s, 19.2 MB of JSON text over FFI |
| 100k rows × 10 declared types — `op=export` (Parquet) | **0.38–0.40 s**, 2.35 MB file (6.8 MB uncompressed) |
| reading that file with `read_parquet` + `count/sum` | 0.002 s |
| fidelity vs. the JSON path | `DATE`=2026-09-11, `TIMESTAMP` local, `BOOLEAN`, `BIGINT` 9007199254740993 exact, `BLOB` `00FF000A0D010203` byte-exact, 100000/100000 rows keep the `\|` character |

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
9. **parquet-go's `date` node expects int32 epoch days**, not a `time.Time`
   whose Unix *seconds* get written verbatim — a DATE column came back as
   `5461899-03-14 (BC)` until the bridge converted to days itself.
10. **`luajit_table` hands the module a string, `luajit_vs` hands it a table**
    (one table per argument, chunk-batched, expecting a table back). The same
    library file must accept both shapes; assuming a string made every
    `usql('<spec>')` macro call die with `attempt to call method 'match'`.

## Status

v0.2.0 — native Parquet export (`op=export`) plus the v0.1.1 baseline: all five
platform artifacts built and smoke-tested in CI. Only the SQLite
(`moderncsqlite`) driver has been exercised end-to-end so far; the other schemes
are one import away but not yet verified against live servers.

## License

MIT — see [LICENSE](LICENSE).
