LOAD '/mnt/d/wsl2/luajit/build/release/luajit.duckdb_extension';

SELECT * FROM luajit_module(mode := 'quick_compile', sql_name := 'usql',
  source := 'return dofile(''/mnt/d/wsl2/tmp/usql-bridge/usql.lua'')');

-- 1) 建一个带「声明类型」的夹具：10 种类型 + 10 万行，其中 t 里带竖线、b 里带非 UTF-8 字节
SELECT val FROM luajit_table('usql', list := '{"op":"connect","url":"moderncsqlite:////tmp/usql_export_dev.db"}');
SELECT val FROM luajit_table('usql', list := '{"op":"exec","id":1,"sql":"DROP TABLE IF EXISTS typ"}');
SELECT val FROM luajit_table('usql', list := '{"op":"exec","id":1,"sql":"CREATE TABLE typ(i INTEGER, big BIGINT, r REAL, d NUMERIC, t TEXT, b BLOB, dt DATE, ts TIMESTAMP, bo BOOLEAN, n TEXT)"}');
SELECT val FROM luajit_table('usql', list := '{"op":"exec","id":1,"sql":"INSERT INTO typ SELECT value, 9007199254740993, value/3.0, value/100.0, ''row-|piped-''||value, x''00FF000A0D010203'', ''2026-09-11'', ''2026-09-11 18:40:12'', value%2=0, NULL FROM (WITH RECURSIVE s(value) AS (SELECT 0 UNION ALL SELECT value+1 FROM s WHERE value < 99999) SELECT value FROM s)"}');
SELECT val FROM luajit_table('usql', list := '{"op":"query","id":1,"sql":"SELECT COUNT(*) AS n FROM typ"}');

-- 2) 列式导出到显式路径（表函数形态 -> 返回文件路径）
SELECT val AS exported_path FROM luajit_table('usql',
  list := '{"op":"export","id":1,"sql":"SELECT * FROM typ","path":"/tmp/usql_export_typed.parquet","format":"parquet","compression":"snappy"}');

-- 3) 标量形态（quick_compile 生成的 usql(...) 宏）+ 自动临时路径，直接喂 read_parquet
SELECT count(*) AS rows, sum(i) AS sum_i,
       count(*) FILTER (WHERE t LIKE '%|%') AS rows_with_pipe,
       count(*) FILTER (WHERE t LIKE '%¦%') AS rows_with_broken_pipe
FROM read_parquet(usql('{"op":"export","id":1,"sql":"SELECT i,t,b FROM typ WHERE i < 5000"}'));

-- 4) 类型直通（不靠 from_json 推断）：bigint/bigint/double/double/varchar/blob/date/timestamp/boolean/varchar
DESCRIBE SELECT * FROM read_parquet('/tmp/usql_export_typed.parquet');

-- 5) 字节与值保真：BLOB 原样、竖线没被替换、NULL 还是 NULL
SELECT i, hex(b) AS b_hex, t, dt, ts, bo, n FROM read_parquet('/tmp/usql_export_typed.parquet') WHERE i = 0;
SELECT count(*) AS n_rows, sum(i) AS sum_i, sum(big) AS sum_big FROM read_parquet('/tmp/usql_export_typed.parquet');
SELECT count(*) AS broken_pipes FROM read_parquet('/tmp/usql_export_typed.parquet') WHERE t LIKE '%¦%';
SELECT count(*) AS nulls_kept FROM read_parquet('/tmp/usql_export_typed.parquet') WHERE n IS NULL;

-- 6) compression=none 对照（同数据，文件应更大）
SELECT val FROM luajit_table('usql', list := '{"op":"export","id":1,"sql":"SELECT * FROM typ","path":"/tmp/usql_export_none.parquet","compression":"none"}');

-- 7) 错误路径：坏 SQL / 不存在的连接 / 不支持的格式
SELECT val FROM luajit_table('usql', list := '{"op":"export","id":1,"sql":"SELECT * FROM no_such_table"}');
SELECT val FROM luajit_table('usql', list := '{"op":"export","id":99,"sql":"SELECT 1"}');
SELECT val FROM luajit_table('usql', list := '{"op":"export","id":1,"sql":"SELECT 1","format":"csv"}');

-- 8) close：自动临时目录里那份应被清掉，显式 path 的两份留着
SELECT val FROM luajit_table('usql', list := '{"op":"close","id":1}');
