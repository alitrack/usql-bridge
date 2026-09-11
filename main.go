package main

import "C"

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/xo/dburl"
	"github.com/xo/usql/drivers"

	_ "github.com/xo/usql/drivers/moderncsqlite" // registers "sqlite" driver (pure Go, no CGO)
)

// Persistent connections keyed by int id, reused across queries => no cold start per query.
var (
	mu     sync.Mutex
	conns  = map[int]*sql.DB{}
	nextID = 0
)

//export usql_connect
func usql_connect(url *C.char) *C.char {
	mu.Lock()
	defer mu.Unlock()
	u, err := dburl.Parse(C.GoString(url))
	if err != nil {
		return C.CString("ERR: bad url: " + err.Error())
	}
	db, err := drivers.Open(context.Background(), u, nil, nil)
	if err != nil {
		return C.CString("ERR: open failed: " + err.Error())
	}
	// Ping forces the connection open NOW, so the cold start happens at connect,
	// not on the first query.
	if err := db.Ping(); err != nil {
		db.Close()
		return C.CString("ERR: ping failed: " + err.Error())
	}
	nextID++
	conns[nextID] = db
	return C.CString(fmt.Sprintf("id=%d", nextID))
}

//export usql_query
func usql_query(id C.int, query *C.char) *C.char {
	mu.Lock()
	db := conns[int(id)]
	mu.Unlock()
	if db == nil {
		return C.CString("ERR: no connection with that id")
	}
	rows, err := db.Query(C.GoString(query))
	if err != nil {
		return C.CString("ERR: " + err.Error())
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return C.CString("ERR: " + err.Error())
	}
	out := []map[string]any{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return C.CString("ERR: " + err.Error())
		}
		row := map[string]any{}
		for i, c := range cols {
			row[c] = normalize(vals[i])
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return C.CString("ERR: " + err.Error())
	}
	b, _ := json.Marshal(out)
	return C.CString(string(b))
}

//export usql_exec
func usql_exec(id C.int, query *C.char) *C.char {
	mu.Lock()
	db := conns[int(id)]
	mu.Unlock()
	if db == nil {
		return C.CString("ERR: no connection with that id")
	}
	res, err := db.Exec(C.GoString(query))
	if err != nil {
		return C.CString("ERR: " + err.Error())
	}
	n, _ := res.RowsAffected()
	return C.CString(fmt.Sprintf("OK rows=%d", n))
}

//export usql_close
func usql_close(id C.int) C.int {
	mu.Lock()
	defer mu.Unlock()
	db := conns[int(id)]
	if db == nil {
		return C.int(0)
	}
	db.Close()
	delete(conns, int(id))
	cleanupExports(int(id))
	return C.int(1)
}

func normalize(v any) any {
	switch t := v.(type) {
	case []byte:
		return string(t)
	case time.Time:
		return t.UTC().Format("2006-01-02T15:04:05Z")
	default:
		return v
	}
}

func main() {}
