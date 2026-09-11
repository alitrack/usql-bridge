# AGENTS.md — usql-bridge

Working contract for AI agents touching this repo. Keep it true: every command
here has been run.

## Build & Test

```bash
./build-release.sh                 # every target this host can build
./build-release.sh linux-amd64     # single target -> dist/usqlbridge-linux-amd64.so
python3 scripts/smoke_test.py dist/usqlbridge-linux-amd64.so   # ctypes round trip
```

End-to-end through DuckDB (needs the duckdb-luajit extension, unsigned):

```bash
duckdb -unsigned -batch < test_usql_bridge.sql
duckdb -unsigned -batch < test_export.sql     # native Parquet export
```

Toolchain: Go >= 1.26.1 (usql's go.mod floor; a newer host Go pulls it via
`GOTOOLCHAIN=go1.26.1+auto`), cgo **must** be enabled, plus a target C
toolchain — `gcc`, `gcc-aarch64-linux-gnu`, `x86_64-w64-mingw32-gcc`, or clang.
Keep `GOMODCACHE`/`GOPATH` off `/mnt/d` (slow 9p mount).

CI (`ci.yml`) builds and smoke-tests every platform on every push; `release.yml`
runs the same matrix on a `v*` tag and publishes the artifacts plus
`SHA256SUMS`. Releases are produced by native runners — darwin cannot be
cross-compiled from Linux.

## Architecture

- `main.go` — the whole bridge. Exports `usql_connect(url) -> "id=N"`
  (Ping at connect, so cold start lands there), `usql_query(id, sql)` and
  `usql_exec(id, sql)` returning JSON / `OK rows=N`, and `usql_close(id)`.
  Open `*sql.DB` handles live in `map[int]*sql.DB` guarded by a mutex.
- `export.go` — `usql_export(spec)` (spec is the whole flat JSON, `id`/`sql`/
  `path`/`format`/`compression`) writes the result set as Parquet from the
  driver's declared column types and returns the file path. Auto-named files
  (no `path` given) are registered per connection and removed in `usql_close`.
  This is the *columnar* channel; the JSON ops are a display channel that loses
  type information and mangles non-UTF-8 bytes.
- Dependency direction: DuckDB `luajit` extension -> `usql.lua` (LuaJIT FFI,
  `ffi.cdef` + `ffi.load`) -> this c-shared library -> usql drivers ->
  `database/sql`. Nothing in the chain knows about the layers above it.
- `usql.lua` also carries the artifact-resolution order: `spec.lib` >
  `USQL_BRIDGE_LIB` > `~/.duckdb/luajit-libs/` > best-effort download from the
  release of the current tag.

## Key Patterns

- One usql driver per backend, self-registered in `init()`: add
  `_ "github.com/xo/usql/drivers/<scheme>"` to `main.go` and rebuild. The
  `drivers.Open(ctx, *dburl.URL, nil, nil)` call is the only usql API used.
- Scheme names are driver names, not guesses: SQLite is `moderncsqlite`
  (pure Go, no CGO at runtime), **not** `sqlite3`.
- Go strings returned to Lua are `C.CString` allocations: the Lua side must
  `ffi.C.free` them, and `free` must be declared in `ffi.cdef`.
- `ffi.cdef` signatures track the `//export` signatures exactly; a mismatch
  shows up as a conversion error at the call site, not at link time.
- Artifact names are fixed: `usqlbridge-<os>-<arch>.{so,dylib,dll}`. The Lua
  wrapper, the workflows' presence check and the release assets all key off
  that pattern — rename in all three places or none.
- `-ldflags "-s -w"` is deliberate: it trims ~30% off the download while the
  exported symbols (which `ffi.load` needs) stay in `.dynsym`.
- Declared type -> Parquet node is a fixed table in `mapColumn`; anything
  unknown falls back to `VARCHAR` on purpose. Do not "improve" that fallback by
  guessing numbers — a wrong guess is silent wrong data.
- Parquet values are built with `parquet.SchemaOf` over a `reflect.StructOf`
  row type (ordered fields; `parquet.Group` is a map and would sort columns
  alphabetically). `*time.Time` + the `date` tag is a trap: parquet-go writes
  Unix *seconds* into a day counter, so DATE goes through `*int32` epoch days.
- A library file is entered two ways: `luajit_table(...)` passes the spec as a
  **string**, `luajit_vs(...)`/the generated `usql(...)` macro passes a
  **table** of that argument's chunk values and expects a table back. Both
  shapes must be handled in the same entry function.

## Risk Gates

- AUTO-APPROVED: editing `main.go`, `export.go`, `usql.lua`,
  `scripts/smoke_test.py`, `test_usql_bridge.sql`, `test_export.sql`, docs.
- REQUIRES APPROVAL: changing `build-release.sh` targets or Artifact names,
  bumping the pinned usql version in `go.mod`, editing `.github/workflows/`.
- BOUNDARY (never touch without being asked): publishing a release or moving a
  tag, changing `LICENSE`, rewriting `.github/RELEASE_NOTES.md` for a release
  that already exists.

## Conventions

- Default branch `main`; remote `git@github.com:alitrack/usql-bridge.git`;
  single-maintainer repo, commit directly to `main`.
- README is English (`README.md`) with a Chinese twin (`README_cn.md`), linked
  from the top of each file; no Chinese characters in `README.md`.
- Version bumps touch `README.md` + `README_cn.md` (Status section) and
  `.github/RELEASE_NOTES.md`, and a patch release ships on its own rather than
  being batched with the next feature release.
- Numbers in the README must come from measured runs (`ls -l`, the benchmark
  in `test_usql_bridge.sql`) — never estimated.
- Release timing and platform-coverage rules follow the maintainer's
  `opensource-repo-conventions` playbook: verify all five artifacts exist
  before declaring a release done.
