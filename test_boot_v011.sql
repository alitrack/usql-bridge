-- Platform-aware bootstrap test: no spec.lib, no USQL_BRIDGE_LIB, empty cache.
-- Expectation: usql.lua picks usqlbridge-linux-amd64.so for this host, finds
-- nothing in the cache, downloads it from the v0.1.1 release, then queries.
LOAD '/mnt/d/wsl2/luajit/build/release/luajit.duckdb_extension';

SELECT * FROM luajit_module(mode := 'quick_compile', sql_name := 'usql',
  source := 'return dofile(''/mnt/d/wsl2/duckdb-luajit-libs/libs/db/usql.lua'')');

SELECT val FROM luajit_table('usql', list := '{"op":"connect","url":"moderncsqlite:////tmp/usql_v011_boot.db"}');
SELECT val FROM luajit_table('usql', list := '{"op":"exec","id":1,"sql":"CREATE TABLE IF NOT EXISTS t(a INTEGER, b TEXT)"}');
SELECT val FROM luajit_table('usql', list := '{"op":"exec","id":1,"sql":"INSERT INTO t VALUES (1,''x''),(2,''y'')"}');
SELECT val FROM luajit_table('usql', list := '{"op":"query","id":1,"sql":"SELECT a, b FROM t ORDER BY a"}');
SELECT val FROM luajit_table('usql', list := '{"op":"benchmark","id":1,"n":200,"sql":"SELECT a, b FROM t"}');
SELECT val FROM luajit_table('usql', list := '{"op":"close","id":1}');
