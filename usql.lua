local ffi = require('ffi')
ffi.cdef[[
  extern char* usql_connect(const char* url);
  extern char* usql_query(int id, const char* query);
  extern char* usql_exec(int id, const char* query);
  extern int   usql_close(int id);
  extern void free(void* ptr);
]]

local LIB = '/mnt/d/wsl2/tmp/usql-bridge/usqlbridge.so'
local lib = ffi.load(LIB)

local function free_ret(s)
  local str = ffi.string(s)
  ffi.C.free(s)
  return str
end

local M = function(p)
  local ok, res = pcall(function()
    if p.op == 'connect' then
      local r = lib.usql_connect(p.url)
      return free_ret(r)
    elseif p.op == 'query' then
      local t0 = os.clock()
      local r = lib.usql_query(p.id, p.sql)
      local t1 = os.clock()
      return string.format('[%.1fms] %s', (t1 - t0) * 1000, free_ret(r))
    elseif p.op == 'query_notime' then
      return free_ret(lib.usql_query(p.id, p.sql))
    elseif p.op == 'exec' then
      return free_ret(lib.usql_exec(p.id, p.sql))
    elseif p.op == 'close' then
      return 'closed=' .. tostring(lib.usql_close(p.id))
    elseif p.op == 'benchmark' then
      -- the money shot: 1 warmup then N queries, report avg
      local n = p.n or 100
      local sql = p.sql or 'SELECT 1 AS x'
      -- warmup (first query after connect)
      local r0 = free_ret(lib.usql_query(p.id, sql))
      local t0 = os.clock()
      for i = 1, n do
        free_ret(lib.usql_query(p.id, sql))
      end
      local t1 = os.clock()
      return string.format('first=%s then %d queries in %.1fms (%.3f ms/each)',
        r0, n, (t1 - t0) * 1000, (t1 - t0) * 1000 / n)
    end
    return 'unknown op'
  end)
  if not ok then return 'ERR: ' .. tostring(res) end
  return res
end
return M
