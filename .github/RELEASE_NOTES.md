In-process Go c-shared bridge to [xo/usql](https://github.com/xo/usql) for the
DuckDB `luajit` extension. Persistent `database/sql` connections behind a
LuaJIT FFI table function: one connect, then no per-query process cold start
(measured ~0.1-0.2 ms/query sustained, vs 30-100 ms for spawning the `usql`
CLI binary for every query).

## Artifacts

| Platform | File |
|---|---|
| Linux x86_64 | `usqlbridge-linux-amd64.so` |
| Linux arm64 | `usqlbridge-linux-arm64.so` |
| macOS Apple Silicon | `usqlbridge-darwin-arm64.dylib` |
| macOS Intel | `usqlbridge-darwin-amd64.dylib` |
| Windows x86_64 | `usqlbridge-windows-amd64.dll` |

Every artifact is built natively on its own OS in CI and smoke-tested there
(`scripts/smoke_test.py` loads it through ctypes — the same C ABI LuaJIT FFI
uses — then does a create/insert/select round trip on SQLite). See
`.github/workflows/release.yml`.

## Drivers

Current release embeds `moderncsqlite` (pure-Go SQLite, **no CGO at runtime**;
the scheme is `moderncsqlite`, not `sqlite3`). To add a backend, import the
matching `github.com/xo/usql/drivers/<scheme>` in `main.go` and rebuild —
every usql driver is a plain `database/sql` driver, that is the whole trick.

## Usage

LuaJIT FFI table function; see the companion DuckDB library
[libs/db/usql.lua](https://github.com/alitrack/duckdb-luajit-libs/blob/main/libs/db/usql.lua):

```sql
SELECT * FROM luajit_module(mode := 'quick_compile', sql_name := 'usql',
  source := 'return dofile(''usql.lua'')');

SELECT luajit_s('usql', {op: 'connect', url: 'moderncsqlite:///tmp/app.db'});
SELECT luajit_s('usql', {op: 'query',   id: 1, sql: 'SELECT 1 AS a'});
SELECT luajit_s('usql', {op: 'exec',    id: 1, sql: 'CREATE TABLE t(a INT)'});
SELECT luajit_s('usql', {op: 'close',   id: 1});
```

The bridge resolves the library from `spec.lib` > `USQL_BRIDGE_LIB` >
`~/.duckdb/luajit-libs/` (install cache) > best-effort download from the
release of the current tag.

## Build from source

```bash
./build-release.sh                 # every target this host can build
./build-release.sh linux-amd64     # a single target
python3 scripts/smoke_test.py dist/usqlbridge-linux-amd64.so
```

**cgo is mandatory.** `main.go` has `import "C"` and uses `//export`, so
`-buildmode=c-shared` requires `CGO_ENABLED=1` plus a C toolchain for the
*target* platform (`gcc`, `gcc-aarch64-linux-gnu`, `x86_64-w64-mingw32-gcc`, or
clang for darwin). `CGO_ENABLED=0` fails with *"build constraints exclude all Go
files"*. macOS artifacts must be built on macOS — no usable darwin cross
toolchain exists on Linux — which is why releases are produced by a native
runner matrix rather than one machine cross-compiling everything.

## License

MIT. See [LICENSE](LICENSE).
