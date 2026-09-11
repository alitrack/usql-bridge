# usql-bridge

in-process **Go c-shared 桥**，把 [xo/usql](https://github.com/xo/usql) 的
`database/sql` 驱动编译进一个动态库，通过 LuaJIT FFI 表函数暴露给 DuckDB
（配合 [duckdb-luajit](https://github.com/alitrack/duckdb-luajit) 扩展）——
**连接常驻、每次查询不拉进程**。

English version: [README.md](README.md)

## 为什么

已有的 `dbcli` 路线每次查询都要拉一个 `usql` 二进制进程：每次 30–100ms 冷启。
本桥把连接留在 DuckDB 进程内，后续查询复用同一条连接：

| 路线 | 单次查询成本 | 依赖 |
|---|---|---|
| `dbcli` + `usql` 二进制 | 30–100ms（进程冷启） | 本机装 `usql` |
| **usql-bridge**（本仓） | **实测持续 ~0.1–0.2ms** | 一个 c-shared 工件，零外部二进制 |

成立的地基只有一条：**usql 每个驱动都是标准 `database/sql` 驱动**，
`drivers.Open(ctx, *dburl.URL, nil, nil)` 直接返回可常驻的 `*sql.DB`。
桥不碰 usql 的 CLI/REPL 内部机制。

边界：主流分析型数据源仍以 `duckdb_universal`（Rust 原生连接器）为主；
本桥补的是中间那道缝——长尾库、低 QPS、不想为它装全局二进制的场景。

## 工件

| 平台 | 文件 |
|---|---|
| Linux x86_64 | `usqlbridge-linux-amd64.so` |
| Linux arm64 | `usqlbridge-linux-arm64.so` |
| macOS Apple Silicon | `usqlbridge-darwin-arm64.dylib` |
| macOS Intel | `usqlbridge-darwin-amd64.dylib` |
| Windows x86_64 | `usqlbridge-windows-amd64.dll` |

## 文件

| 文件 | 作用 |
|---|---|
| `main.go` | 桥本体：`//export usql_connect / usql_query / usql_exec / usql_close` |
| `usql.lua` | LuaJIT FFI 封装（`ffi.cdef` + `ffi.load`），结果以 JSON 串返回 |
| `build-release.sh` | 按目标平台编 c-shared 工件 |
| `scripts/smoke_test.py` | 用 ctypes 加载工件跑一轮 SQL 往返——每个平台的 CI 门 |
| `test_usql_bridge.sql` | 全链路：DuckDB → luajit → 桥 → SQLite，含 200 次查询基准 |
| `.github/workflows/` | `ci.yml`（每次 push 编译+冒烟）、`release.yml`（打 tag 出全平台 release） |

## 用法

```sql
LOAD 'luajit.duckdb_extension';

SELECT * FROM luajit_module(mode := 'quick_compile', sql_name := 'usql',
  source := 'return dofile(''usql.lua'')');

-- 本库是表函数：桥返回的每个 JSON 对象 = 一行
SELECT val FROM luajit_table('usql', list := '{"op":"connect","url":"moderncsqlite:////tmp/app.db"}');
SELECT val FROM luajit_table('usql', list := '{"op":"query","id":1,"sql":"SELECT id, name FROM src ORDER BY id"}');
SELECT val FROM luajit_table('usql', list := '{"op":"exec","id":1,"sql":"INSERT INTO src VALUES (9, ''zeta'')"}');
SELECT val FROM luajit_table('usql', list := '{"op":"benchmark","id":1,"n":200,"sql":"SELECT 1"}');
SELECT val FROM luajit_table('usql', list := '{"op":"close","id":1}');
```

（`quick_compile` 同时生成便捷宏，可直接 `SELECT * FROM usql('{"op":"connect",...}')`。）

连接 id 在同一个 DuckDB 进程内跨调用持久（Go 侧 `map[int]*sql.DB`）；
出错返回单行 `ERR: <原因>`。

## 驱动（scheme）

当前 release 内嵌 **`moderncsqlite`**（纯 Go SQLite，**运行期零 CGO**）。
scheme 是 `moderncsqlite` 而**不是** `sqlite3`——这是两个不同驱动，用错连不上。

usql 驱动在 `init()` 自注册，所以加一个库就是加一行 import 再重编：

```go
_ "github.com/xo/usql/drivers/postgres"   // 之后可连 postgres://user@host/db
```

DSN 语法同 `github.com/xo/dburl`，也就是 `usql` CLI 接受的那种 URL。

## 从源码构建

```bash
./build-release.sh                  # 本机能编的目标全编（其余跳过并提示）
./build-release.sh linux-amd64      # 单个目标
python3 scripts/smoke_test.py dist/usqlbridge-linux-amd64.so
```

要求 Go ≥ 1.26.1（usql 的 go.mod 下限）、cgo 打开、目标平台有 C 工具链。

**cgo 是硬要求**：`main.go` 有 `import "C"` 且用 `//export`，
`-buildmode=c-shared` 必须 `CGO_ENABLED=1`；设成 0 会直接报
*"build constraints exclude all Go files"*。darwin 需要一个 Mach-O 链接器和
Apple SDK，Linux 上不存在可用的 darwin 交叉工具链——所以 release 走
**各 OS 原生 runner 矩阵**（`.github/workflows/release.yml`），不是一台机器
交叉编译所有平台。

## 取工件与校验

`usql.lua` 的解析顺序：`spec.lib` → `USQL_BRIDGE_LIB` →
`~/.duckdb/luajit-libs/usqlbridge-<os>-<arch>.<ext>` → 从 v0.1.1 release 拉一次。
下载必须同时过「大小下限 + 平台头魔数」（ELF / PE / Mach-O）才算成功——release
CDN 会静默截断（实测 11.2MB 的工件 6m25s 只落地 7.6MB，重试 0 字节，而同一时间
raw 侧 1s 返回）。

release CDN 慢或不通时，把下载基址指到任意镜像前缀（结尾必须带 `/`）：

```bash
export USQL_BRIDGE_BASE_URL='https://gh-proxy.com/https://github.com/alitrack/usql-bridge/releases/download/v0.1.1/'
```

或者自己下 + 对官方校验和：

```bash
gh release download v0.1.1 --repo alitrack/usql-bridge
sha256sum -c SHA256SUMS            # 11.2MB 工件按此校验通过
cp usqlbridge-linux-amd64.so ~/.duckdb/luajit-libs/
```

## 实测

环境：go1.26.1、DuckDB 1.5.5、luajit ELF 扩展、SQLite（`moderncsqlite`）。

| 项 | 结果 |
|---|---|
| connect + Ping | `id=1`，冷启前置到 connect |
| CREATE / INSERT / SELECT / 聚合 / 写读回 / close | 全对 |
| 单次 query（含 FFI 跨语言 + JSON 编码） | 0.1–0.3ms |
| 200 次持续 query | ~0.2ms/次，无冷启、无退化 |

## 踩过的坑

1. **`C.CString` 必须 Lua 侧 `ffi.C.free`**，且 `free` 要在 `ffi.cdef` 里显式声明。
2. **`ffi.cdef` 要和 `//export` 签名严格一致**：connect 从 `C.int` 改成 `*C.char`
   后 cdef 没跟改，报 `cannot convert 'number' to 'const char *'`——连接其实成功了。
3. **`local ok = pcall(ffi.load, p)` 只取到布尔**，库对象是第二个返回值。
4. **Lua 闭包只能看到定义在它前面的 `local`**，后面的 `local` 会被当全局 → 调用时 nil。
5. **usql 的 SQLite scheme 是 `moderncsqlite`**，不是 `sqlite3`。
6. **DuckDB `COPY (FORMAT CSV)` 出的 `.db` 不是 SQLite 库**，扩展名骗人；
   测试让驱动自己 `CREATE TABLE` 建库。
7. **GitHub release 的 CDN ≠ raw 的 CDN**，两者会独立抽风：release 拉 15MB 卡住时
   raw 可能秒回。所以自动下载是 best-effort，失败给出「手动放到哪」的 ERR 提示。
8. **`/mnt/d` 是慢的 9p 盘**：`GOMODCACHE`/`GOPATH` 放 WSL 本地盘。

## 状态

v0.1.1 —— 五个平台工件全部由 CI 原生构建 + 冒烟通过。目前只有 SQLite
（`moderncsqlite`）驱动跑通了端到端；其他 scheme 只差一行 import，但尚未对真库验证。

## 协议

MIT，见 [LICENSE](LICENSE)。
