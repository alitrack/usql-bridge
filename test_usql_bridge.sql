LOAD '/mnt/d/wsl2/luajit/build/release/luajit.duckdb_extension';

SELECT * FROM luajit_module(mode := 'quick_compile', sql_name := 'usql',
  source := 'return dofile(''/mnt/d/wsl2/tmp/usql-bridge/usql.lua'')');

-- 1) connect (cold start happens here via Ping)
SELECT val AS connect FROM luajit_table('usql', list := '{"op":"connect","url":"moderncsqlite:////tmp/usql_bridge_dev.db"}');

-- 2) usql creates the table + data itself (no external fixture needed)
SELECT val FROM luajit_table('usql', list := '{"op":"exec","id":1,"sql":"DROP TABLE IF EXISTS src"}');
SELECT val FROM luajit_table('usql', list := '{"op":"exec","id":1,"sql":"CREATE TABLE src(id INTEGER, name TEXT, score REAL)"}');
SELECT val FROM luajit_table('usql', list := '{"op":"exec","id":1,"sql":"INSERT INTO src VALUES (1,''alpha'',10.5),(2,''beta'',20.25),(3,''gamma'',30.75),(4,''delta'',40.125),(5,''eps'',50.0)"}');

-- 3) a query through the usql bridge (one row = one JSON object)
SELECT val FROM luajit_table('usql', list := '{"op":"query","id":1,"sql":"SELECT id, name, score FROM src ORDER BY id"}');

-- 4) an aggregate
SELECT val FROM luajit_table('usql', list := '{"op":"query","id":1,"sql":"SELECT COUNT(*) AS n, AVG(score) AS avg_score FROM src"}');

-- 5) a write + read-back through the bridge
SELECT val FROM luajit_table('usql', list := '{"op":"exec","id":1,"sql":"INSERT INTO src VALUES (6,''zeta'',60.5)"}');
SELECT val FROM luajit_table('usql', list := '{"op":"query","id":1,"sql":"SELECT COUNT(*) AS n FROM src"}');

-- 6) THE MONEY SHOT: sustained query latency on the persistent connection
SELECT val FROM luajit_table('usql', list := '{"op":"benchmark","id":1,"n":200,"sql":"SELECT id, name, score FROM src WHERE score > 25 ORDER BY score"}');

-- 7) close
SELECT val FROM luajit_table('usql', list := '{"op":"close","id":1}');
