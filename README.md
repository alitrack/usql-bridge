# usql-bridge PoC

验证「Go c-shared 内嵌 usql 核心 → 常驻 database/sql 连接 → LuaJIT FFI 桥 → DuckDB UDF」这条 in-process 路线。
定位：补「不要用户装 usql 二进制、又要 DuckDB 进程内常驻连接」的中间缝（长尾库低 QPS 分析，主流走 duckdb_universal Rust 原生）。

## 文件
- `main.go` — Go 桥，`//export usql_connect/usql_query/usql_exec/usql_close`
  - 连接按 int id 常驻复用（`map[int]*sql.DB`），**多次 query 无冷启**
  - `usql_connect` 内 `Ping()` 把冷启动前置到 connect，首个 query 不背冷启
  - import `_ github.com/xo/usql/drivers/moderncsqlite`（纯 Go SQLite，免 CGO）
  - 结果 JSON 编码返回（`[]map[string]any`，列名做 key，`[]byte`→string、`time.Time`→RFC3339 归一化）
- `usql.lua` — LuaJIT FFI 桥：`ffi.cdef` 声明 + `ffi.load`，返回串必须 `ffi.C.free`（cdef 要先声明 `extern void free(void*)`）
- `test_usql_bridge.sql` — quick_compile + 全链路冒烟 + 基准
- `build.sh` — `go mod tidy` + `go build -buildmode=c-shared`（GOTOOLCHAIN=go1.26.1，usql go.mod 要 1.26）
- `usql-src/` — 本地 usql 源码（curl codeload tarball 拉的，git clone 走 GnuTLS 挂）

## 架构关键点
- **usql 每个驱动都是标准 `database/sql` driver**，`drivers.Open(ctx, *dburl.URL, nil, nil)` 直接返回可常驻的 `*sql.DB`。这是整条路线成立的地基——桥不用碰 usql 的 CLI/REPL 内部机制，纯走 `database/sql`。
- 驱动 `init()` 时自注册，桥里 import 一个即得一个；要支持哪个库就 import 对应 `drivers/<scheme>`。
- **scheme 不是路径猜的**：`moderncsqlite`（纯 Go）vs `sqlite3`（mattn/CGO）是不同 scheme，连接串前缀必须对。

## 实测（go1.25.8/1.26 toolchain, duckdb 1.5.5, luajit ELF, SQLite via moderncsqlite）
| 项 | 结果 |
|---|---|
| connect + Ping | `id=1`，冷启前置到这里 |
| CREATE/INSERT/SELECT/聚合/写读回/close | 全对 ✅ |
| 单次 query 延迟 | **0.1~0.3ms**（含 FFI 跨语言 + JSON 编码） |
| **200 次持续 query** | **17~21ms = ~0.1ms/次**，无冷启、无退化 ✅（核心卖点） |

## 踩的坑
1. **Go 返回 `C.CString`，Lua 侧必须 `ffi.C.free`，且 `free` 要在 cdef 显式声明**（否则 `missing declaration for symbol 'free'`）。
2. **`ffi.cdef` 的返回类型要和 Go `//export` 签名严格一致**：connect 从 `C.int` 改成 `*C.char`（带错误信息）后，Lua cdef 没跟着改 → `bad argument #1 to 'string' (cannot convert 'number' to 'const char *')`。连接其实成功了，只是显示层错。
3. **usql 的 sqlite scheme 是 `moderncsqlite` 不是 `sqlite3`**，用错连不上。
4. **别拿 DuckDB `COPY ... (FORMAT CSV)` 出的文件当 SQLite 库**：扩展名 `.db` 骗人，内容是 CSV，SQLite 驱动按二进制格式打开直接失败。测试让 usql 自己 `CREATE TABLE` 建库，不依赖外部 fixture。
5. **构建环境**：`/mnt/d` 是 9p 慢盘，GOMODCACHE/GOPATH 放 WSL 本地盘（`/home/lhy/`）；usql go.mod 要 go≥1.26.1，本机 1.25.8 需 `GOTOOLCHAIN=go1.26.1+auto` 拉工具链。`wsl bash -lc` 从 Windows bash 起后台不可靠，长构建用前台 + 高 timeout。

## 结论
in-process 路线 **FEASIBLE**，形态确认 = **labs 一个 `usql.lua` + 一个 Go c-shared 桥（按平台出 .so/.dll/.dylib）**，不新建扩展。
- 单查询 ~0.1ms 已比「每查询拉 usql 二进制」进程冷启（30-100ms）快 2-3 个数量级，且连接常驻。
- 代价：Go 桥要按平台出二进制工件（.so/.dll/.dylib × amd64/arm64），这是唯一真实成本；labs 目前是纯 .lua 拉取，二进制是新类别（建议 GitHub release assets 按 tag 拉）。
- 触发升级成真·扩展的条件：需要 typed 结果集（binary 类型解码）/ 连接级 AST 写保护 / QPS 高到 0.1ms 的 FFI+JSON 开销都嫌慢。
