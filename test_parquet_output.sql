LOAD '/mnt/d/wsl2/luajit/build/release/luajit.duckdb_extension';
.timer on

SELECT * FROM luajit_module(mode := 'quick_compile', sql_name := 'usql',
  source := 'return dofile(''/mnt/d/wsl2/tmp/usql-bridge/usql.lua'')');
SELECT val FROM luajit_table('usql', list := '{"op":"connect","url":"moderncsqlite:////tmp/bench.db"}');
CREATE TEMP TABLE raw AS
SELECT val FROM luajit_table('usql', list := '{"op":"query","id":1,"sql":"SELECT a,b,c FROM t"}');

-- A) 'auto' 结构串是否被支持
SELECT count(*) AS a_rows FROM (SELECT from_json(val, 'auto') AS s FROM raw) t;

-- B) unnest 后按字段取列（显式）
SELECT count(*) AS b_rows, sum(t.s.a) AS b_sum FROM
  (SELECT unnest(from_json(val, '[{"a":"BIGINT","b":"VARCHAR","c":"DOUBLE"}]')) AS s FROM raw) t;

-- C) unnest 后 struct.* 展开
CREATE TEMP TABLE typed AS
SELECT s.* FROM (SELECT unnest(from_json(val, '[{"a":"BIGINT","b":"VARCHAR","c":"DOUBLE"}]')) AS s FROM raw) t;
SELECT count(*) AS c_rows, sum(a) AS c_sum, min(b) AS c_min_b FROM typed;

-- D) 写 Parquet + 读回
COPY typed TO '/tmp/usql_out.parquet' (FORMAT PARQUET);
SELECT count(*) AS d_rows, sum(a) AS d_sum FROM read_parquet('/tmp/usql_out.parquet');

SELECT val FROM luajit_table('usql', list := '{"op":"close","id":1}');
