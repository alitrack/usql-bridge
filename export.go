package main

// usql_export：把查询结果按「列式 + 带类型」写成 Parquet 文件，返回文件路径。
//
// 为什么不是 JSON：JSON 只有 number/string/bool/null，类型要靠 DuckDB 侧推断
// （DATE/DATETIME 变 VARCHAR、BOOLEAN 变整数、INTEGER 变 UBIGINT、DECIMAL 变 DOUBLE），
// 且文本化会把非 UTF-8 字节（BLOB）变成 U+FFFD（不可逆）。列式直出按 driver 声明的
// 类型建 Parquet schema，字节原样、类型原样，DuckDB 侧 read_parquet 零解析。
//
// 规格（同一个扁平 JSON，与其它 op 一致）：
//   {"op":"export","id":1,"sql":"SELECT ...","format":"parquet",
//    "path":"<可选>","compression":"snappy|none","row_group_rows":<可选>}
// 返回：文件路径（成功）/ "ERR: <原因>"（失败）。

import "C"

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress/snappy"
	"github.com/parquet-go/parquet-go/compress/uncompressed"
)

type exportSpec struct {
	ID           int    `json:"id"`
	SQL          string `json:"sql"`
	Path         string `json:"path"`
	Format       string `json:"format"`
	Compression  string `json:"compression"`
	RowGroupRows int    `json:"row_group_rows"`
}

// 自动命名的导出文件登记在连接上，close 时清理（调用方显式给 path 的不动）。
var (
	expMu    sync.Mutex
	expFiles = map[int][]string{}
)

func rememberExport(id int, path string) {
	expMu.Lock()
	expFiles[id] = append(expFiles[id], path)
	expMu.Unlock()
}

func cleanupExports(id int) {
	expMu.Lock()
	paths := expFiles[id]
	delete(expFiles, id)
	expMu.Unlock()
	for _, p := range paths {
		os.Remove(p)
	}
}

var (
	tInt32   = reflect.TypeOf(int32(0))
	tInt64   = reflect.TypeOf(int64(0))
	tFloat64 = reflect.TypeOf(float64(0))
	tString  = reflect.TypeOf("")
	tBytes   = reflect.TypeOf([]byte(nil))
	tBool    = reflect.TypeOf(false)
	tTime    = reflect.TypeOf(time.Time{})
)

// 声明类型 -> (Go 字段类型, parquet tag 选项, 值转换 kind)
//
// 一律用指针类型 + optional：SQL 列基本都可空，指针能表达 NULL。
// 未知/空声明类型兜底成文本，不猜数值（猜错就是静默错数据）。
func mapColumn(c *sql.ColumnType) (reflect.Type, string, string) {
	dbt := strings.ToUpper(strings.TrimSpace(c.DatabaseTypeName()))
	if i := strings.IndexByte(dbt, '('); i > 0 { // DECIMAL(10,2) -> DECIMAL
		dbt = strings.TrimSpace(dbt[:i])
	}
	if f := strings.Fields(dbt); len(f) > 0 { // DOUBLE PRECISION -> DOUBLE
		dbt = f[0]
	}
	switch dbt {
	case "INTEGER", "INT", "INT4", "INT8", "BIGINT", "SMALLINT", "INT2", "TINYINT",
		"MEDIUMINT", "SERIAL", "BIGSERIAL", "SMALLSERIAL", "YEAR", "ROWID", "OID", "MSISDN":
		return reflect.PtrTo(tInt64), ",optional", "int"
	case "REAL", "DOUBLE", "FLOAT", "FLOAT4", "FLOAT8", "NUMERIC", "DECIMAL", "DEC", "MONEY":
		// DECIMAL/NUMERIC 走 DOUBLE：SQLite 的 NUMERIC 列本身就动态（实测驱动对含小数的
		// NUMERIC 列报 int64），精度 <15 位的十进制值无损；需要严格 DECIMAL 逻辑类型的
		// 场景（大额金额），后续按 DecimalSize + decimal(scale,precision) 再加固。
		return reflect.PtrTo(tFloat64), ",optional", "float"
	case "BLOB", "BYTEA", "BINARY", "VARBINARY", "IMAGE", "BYTES", "LONGBLOB", "MEDIUMBLOB", "TINYBLOB":
		return reflect.PtrTo(tBytes), ",optional", "bytes"
	case "BOOLEAN", "BOOL", "BIT":
		return reflect.PtrTo(tBool), ",optional", "bool"
	case "DATE":
		// DATE 走 int32 + date 逻辑类型（epoch 天数）。实测 parquet-go 对 *time.Time 的
		// date 节点会把 Unix 秒当天数写进去（读回来是 5461899 BC），所以自己换算天数。
		return reflect.PtrTo(tInt32), ",optional,date", "date"
	case "DATETIME", "TIMESTAMP", "SMALLDATETIME", "TIMESTAMPTZ", "TIMESTAMP_TZ", "TIMESTAMP WITH TIME ZONE":
		tz := "local"
		if strings.Contains(c.DatabaseTypeName(), "TZ") || strings.Contains(strings.ToUpper(c.DatabaseTypeName()), "WITH TIME ZONE") {
			tz = "utc"
		}
		return reflect.PtrTo(tTime), ",optional,timestamp(microsecond:" + tz + ")", "time"
	default:
		return reflect.PtrTo(tString), ",optional", "str"
	}
}

// parquet tag 里的列名不能带逗号/等号/引号（tag 语法用它们做分隔），退化成下划线。
func tagName(name string) string {
	if name == "" {
		return "_"
	}
	return strings.Map(func(r rune) rune {
		switch r {
		case ',', '=', '"':
			return '_'
		}
		return r
	}, name)
}

func timeLayouts(kind string) []string {
	if kind == "date" {
		return []string{"2006-01-02", "2006-01-02 15:04:05", time.RFC3339, "2006-01-02T15:04:05.999999999"}
	}
	_ = kind
	return []string{
		"2006-01-02 15:04:05.999999999-07:00", "2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05", "2006-01-02T15:04:05.999999999", time.RFC3339, "2006-01-02",
	}
}

func toTimeValue(v any, kind string) (time.Time, error) {
	switch x := v.(type) {
	case time.Time:
		return x, nil
	case string:
		return parseTimeStr(x, kind)
	case []byte:
		return parseTimeStr(string(x), kind)
	case int64:
		return time.Unix(x, 0).UTC(), nil
	case float64:
		return time.Unix(int64(x), 0).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("cannot read %T as %s", v, kind)
}

// epoch 天数（DATE 逻辑类型）
func dateDays(t time.Time) int32 {
	u := t.UTC().Unix()
	d := u / 86400
	if u < 0 && u%86400 != 0 { // 1970 之前：向负无穷取整
		d--
	}
	return int32(d)
}

func toDateDays(v any) (int32, error) {
	switch x := v.(type) {
	case time.Time:
		return dateDays(x), nil
	case string:
		t, err := parseTimeStr(x, "date")
		if err != nil {
			return 0, err
		}
		return dateDays(t), nil
	case []byte:
		return toDateDays(string(x))
	case int64:
		// 已是天数（今天量级 ~2e4）直接用；看着像秒（|x|>1e5）才换算。
		if x > 100000 || x < -100000 {
			d := x / 86400
			if x < 0 && x%86400 != 0 {
				d--
			}
			return int32(d), nil
		}
		return int32(x), nil
	case float64:
		return toDateDays(int64(x))
	}
	return 0, fmt.Errorf("cannot read %T as date", v)
}

func parseTimeStr(s string, kind string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, fmt.Errorf("empty %s value", kind)
	}
	for _, l := range timeLayouts(kind) {
		if t, err := time.Parse(l, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse %q as %s", s, kind)
}

func setCol(dst reflect.Value, kind string, v any) error {
	if v == nil {
		return nil // 保持 nil 指针 => parquet NULL
	}
	pv := reflect.New(dst.Type().Elem())
	e := pv.Elem()
	switch kind {
	case "int":
		n, err := toInt64(v)
		if err != nil {
			return err
		}
		e.SetInt(n)
	case "float":
		f, err := toFloat64(v)
		if err != nil {
			return err
		}
		e.SetFloat(f)
	case "str":
		e.SetString(toString(v))
	case "bytes":
		b, err := toBytesValue(v)
		if err != nil {
			return err
		}
		e.SetBytes(b)
	case "bool":
		b, err := toBool(v)
		if err != nil {
			return err
		}
		e.SetBool(b)
	case "time":
		t, err := toTimeValue(v, kind)
		if err != nil {
			return err
		}
		e.Set(reflect.ValueOf(t))
	case "date":
		d, err := toDateDays(v)
		if err != nil {
			return err
		}
		e.SetInt(int64(d))
	default:
		e.SetString(toString(v))
	}
	dst.Set(pv)
	return nil
}

func toInt64(v any) (int64, error) {
	switch x := v.(type) {
	case int64:
		return x, nil
	case float64:
		return int64(x), nil
	case bool:
		if x {
			return 1, nil
		}
		return 0, nil
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		if err == nil {
			return n, nil
		}
		f, ferr := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if ferr != nil {
			return 0, fmt.Errorf("cannot read %q as integer", x)
		}
		return int64(f), nil
	case []byte:
		return toInt64(string(x))
	}
	return 0, fmt.Errorf("cannot read %T as integer", v)
}

func toFloat64(v any) (float64, error) {
	switch x := v.(type) {
	case float64:
		return x, nil
	case int64:
		return float64(x), nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		if err != nil {
			return 0, fmt.Errorf("cannot read %q as double", x)
		}
		return f, nil
	case []byte:
		return toFloat64(string(x))
	}
	return 0, fmt.Errorf("cannot read %T as double", v)
}

func toString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	case time.Time:
		return x.UTC().Format(time.RFC3339Nano)
	case nil:
		return ""
	default:
		return fmt.Sprint(x)
	}
}

func toBytesValue(v any) ([]byte, error) {
	switch x := v.(type) {
	case []byte:
		out := make([]byte, len(x))
		copy(out, x)
		return out, nil
	case string:
		return []byte(x), nil
	case nil:
		return nil, nil
	}
	return nil, fmt.Errorf("cannot read %T as blob", v)
}

func toBool(v any) (bool, error) {
	switch x := v.(type) {
	case bool:
		return x, nil
	case int64:
		return x != 0, nil
	case float64:
		return x != 0, nil
	case string:
		s := strings.TrimSpace(strings.ToLower(x))
		switch s {
		case "1", "t", "true", "yes", "y", "on":
			return true, nil
		case "0", "f", "false", "no", "n", "off":
			return false, nil
		}
		return false, fmt.Errorf("cannot read %q as boolean", x)
	case []byte:
		return toBool(string(x))
	}
	return false, fmt.Errorf("cannot read %T as boolean", v)
}

//export usql_export
func usql_export(specC *C.char) *C.char {
	var s exportSpec
	if err := json.Unmarshal([]byte(C.GoString(specC)), &s); err != nil {
		return C.CString("ERR: bad export spec: " + err.Error())
	}
	if s.ID == 0 {
		return C.CString("ERR: export needs id (call connect first)")
	}
	if s.SQL == "" {
		return C.CString("ERR: export needs sql")
	}
	if s.Format != "" && s.Format != "parquet" {
		return C.CString("ERR: unsupported format " + s.Format + " (only parquet)")
	}
	mu.Lock()
	db := conns[s.ID]
	mu.Unlock()
	if db == nil {
		return C.CString("ERR: no connection with that id")
	}
	path, err := exportParquet(db, s)
	if err != nil {
		return C.CString("ERR: " + err.Error())
	}
	return C.CString(path)
}

const exportBatchRows = 8192

func exportParquet(db *sql.DB, s exportSpec) (string, error) {
	auto := false
	path := s.Path
	if path == "" {
		path = filepath.Join(os.TempDir(),
			fmt.Sprintf("usql-export-%d-%d.parquet", os.Getpid(), time.Now().UnixNano()))
		auto = true
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
	}

	rows, err := db.Query(s.SQL)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	cts, err := rows.ColumnTypes()
	if err != nil {
		return "", err
	}
	n := len(cts)
	if n == 0 {
		return "", fmt.Errorf("query returned no columns")
	}
	fields := make([]reflect.StructField, n)
	kinds := make([]string, n)
	for i, c := range cts {
		ft, opts, kind := mapColumn(c)
		kinds[i] = kind
		fields[i] = reflect.StructField{
			Name: fmt.Sprintf("F%d", i),
			Type: ft,
			Tag:  reflect.StructTag(`parquet:"` + tagName(c.Name()) + opts + `"`),
		}
	}
	var st reflect.Type
	func() {
		defer func() { _ = recover() }()
		st = reflect.StructOf(fields)
	}()
	if st == nil {
		return "", fmt.Errorf("cannot build row type from %d columns", n)
	}
	schema := parquet.SchemaOf(reflect.New(st).Elem().Interface())

	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		if !ok {
			f.Close()
			os.Remove(path)
		}
	}()

	opts := []parquet.WriterOption{schema, parquet.Compression(&uncompressed.Codec{})}
	switch strings.ToLower(s.Compression) {
	case "snappy", "":
		opts[1] = parquet.Compression(&snappy.Codec{})
	case "none", "uncompressed":
	}
	if s.RowGroupRows > 0 {
		opts = append(opts, parquet.MaxRowsPerRowGroup(int64(s.RowGroupRows)))
	}
	w := parquet.NewWriter(f, opts...)

	vals := make([]any, n)
	ptrs := make([]any, n)
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	batch := make([]parquet.Row, 0, exportBatchRows)
	live := make([]reflect.Value, 0, exportBatchRows) // 保持底层结构存活（Row 里的字节引用它）
	var rowNo int64
	for rows.Next() {
		rowNo++
		if err := rows.Scan(ptrs...); err != nil {
			return "", err
		}
		v := reflect.New(st).Elem()
		for i := range vals {
			if err := setCol(v.Field(i), kinds[i], vals[i]); err != nil {
				return "", fmt.Errorf("column %q row %d: %w", cts[i].Name(), rowNo, err)
			}
			vals[i] = nil
		}
		batch = append(batch, schema.Deconstruct(nil, v.Interface()))
		live = append(live, v)
		if len(batch) >= exportBatchRows {
			if _, err := w.WriteRows(batch); err != nil {
				return "", err
			}
			batch = batch[:0]
			live = live[:0]
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(batch) > 0 {
		if _, err := w.WriteRows(batch); err != nil {
			return "", err
		}
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	ok = true
	if auto {
		rememberExport(s.ID, path)
	}
	return path, nil
}
