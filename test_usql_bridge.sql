LOAD '/mnt/d/wsl2/luajit/build/release/luajit.duckdb_extension';

SELECT * FROM luajit_module(mode := 'quick_compile', sql_name := 'usql',
  source := 'return dofile(''/mnt/d/wsl2/tmp/usql-bridge/usql.lua'')');

-- 1) connect (cold start happens here via Ping)
SELECT luajit_s('usql', {op: 'connect', url: 'moderncsqlite:///mnt/d/wsl2/tmp/usql-bridge/test.db'}) AS connect;

-- 2) usql creates the table + data itself (no external fixture needed)
SELECT luajit_s('usql', {op: 'exec', id: 1, sql: 'CREATE TABLE IF NOT EXISTS src(id INTEGER, name TEXT, score REAL)'}) AS c2;
SELECT luajit_s('usql', {op: 'exec', id: 1, sql: 'INSERT INTO src VALUES (1,''alpha'',10.5),(2,''beta'',20.25),(3,''gamma'',30.75),(4,''delta'',40.125),(5,''eps'',50.0)'}) AS ins;

-- 3) a query through the usql bridge
SELECT luajit_s('usql', {op: 'query', id: 1, sql: 'SELECT id, name, score FROM src ORDER BY id'}) AS q1;

-- 4) an aggregate
SELECT luajit_s('usql', {op: 'query', id: 1, sql: 'SELECT COUNT(*) AS n, AVG(score) AS avg_score FROM src'}) AS q2;

-- 5) a write + read-back through the bridge
SELECT luajit_s('usql', {op: 'exec', id: 1, sql: 'INSERT INTO src VALUES (6,''zeta'',60.5)'}) AS exec1;
SELECT luajit_s('usql', {op: 'query', id: 1, sql: 'SELECT COUNT(*) AS n FROM src'}) AS q3;

-- 6) THE MONEY SHOT: sustained query latency on the persistent connection
SELECT luajit_s('usql', {op: 'benchmark', id: 1, n: 200, sql: 'SELECT id, name, score FROM src WHERE score > 25 ORDER BY score'}) AS bench;

-- 7) close
SELECT luajit_s('usql', {op: 'close', id: 1}) AS close;
