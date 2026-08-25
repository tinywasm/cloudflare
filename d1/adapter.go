//go:build wasm

package d1

import (
	"syscall/js"

	"github.com/tinywasm/await"
	"github.com/tinywasm/ddl"
	"github.com/tinywasm/jsvalue"
	"github.com/tinywasm/model"
	"github.com/tinywasm/orm"
	"github.com/tinywasm/sqlt"
	"github.com/tinywasm/storage"
)

type adapter struct {
	dbObj js.Value
	storage.Compiler
}

// NewEdge opens the named D1 binding (Cloudflare edge runtime) and returns an *orm.DB.
func NewEdge(bindingName string) (*orm.DB, error) {
	v := js.Global().Get("context").Get("env").Get(bindingName)
	if v.IsUndefined() || v.IsNull() {
		return nil, ErrDatabaseNotFound
	}
	return orm.New(&adapter{dbObj: v, Compiler: sqlt.NewCompiler()}), nil
}

// CompileDDL forwards to the underlying sqlt compiler, so a caller can migrate through
// ddl.New(db.RawConn(), db.RawConn().(ddl.Compiler)) exactly like the sqlite/postgres adapters
// (see sqlite.DDLCompiler). storage.Compiler above only exposes read/write compilation, not DDL,
// so it is forwarded explicitly rather than promoted.
func (a *adapter) CompileDDL(s ddl.Stmt, m model.Model) (string, []any, error) {
	return sqlt.NewCompiler().CompileDDL(s, m)
}

// DDLCompiler returns the ddl.Compiler half of the connection, for callers wiring up
// ddl.New(conn, d1.DDLCompiler(conn)) — mirrors sqlite.DDLCompiler.
func DDLCompiler(conn storage.Conn) (ddl.Compiler, bool) {
	c, ok := conn.(ddl.Compiler)
	return c, ok
}

func (a *adapter) Exec(query string, args ...any) error {
	stmt := a.dbObj.Call("prepare", query)
	_, err := await.Promise(bindArgs(stmt, args).Call("run"))
	return err
}

func (a *adapter) QueryRow(query string, args ...any) storage.Scanner {
	stmt := a.dbObj.Call("prepare", query)
	opts := js.Global().Get("Object").New()
	opts.Set("columnNames", true)
	arr, err := await.Promise(bindArgs(stmt, args).Call("raw", opts))
	if err != nil {
		return &errScanner{err}
	}
	if arr.Length() < 2 {
		return &errScanner{orm.ErrNotFound}
	}
	return &rowScanner{arr.Index(1)}
}

func (a *adapter) Query(query string, args ...any) (storage.Rows, error) {
	stmt := a.dbObj.Call("prepare", query)
	opts := js.Global().Get("Object").New()
	opts.Set("columnNames", true)
	arr, err := await.Promise(bindArgs(stmt, args).Call("raw", opts))
	if err != nil {
		return nil, err
	}
	if arr.Length() == 0 {
		return &d1Rows{}, nil
	}
	colsJS := arr.Call("shift")
	cols := make([]string, colsJS.Length())
	for i := range cols {
		cols[i] = colsJS.Index(i).String()
	}
	return &d1Rows{arr: arr, cols: cols}, nil
}

func (a *adapter) Close() error { return nil }

func bindArgs(stmt js.Value, args []any) js.Value {
	if len(args) == 0 {
		return stmt
	}
	jsArgs := make([]any, len(args))
	for i, arg := range args {
		if b, ok := arg.([]byte); ok {
			ua := jsvalue.Uint8ArrayClass.New(len(b))
			js.CopyBytesToJS(ua, b)
			jsArgs[i] = ua
		} else {
			jsArgs[i] = arg
		}
	}
	return stmt.Call("bind", jsArgs...)
}
