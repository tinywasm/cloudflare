//go:build wasm

package d1

import (
	"syscall/js"

	. "github.com/tinywasm/fmt"
	"github.com/tinywasm/jsvalue"
	"github.com/tinywasm/storage"
)

type d1Rows struct {
	arr  js.Value
	cols []string
	cur  int
	len  int
	once bool
}

func (r *d1Rows) rowsLen() int {
	if !r.once {
		if r.arr.Truthy() {
			r.len = r.arr.Length()
		}
		r.once = true
	}
	return r.len
}

func (r *d1Rows) Columns() ([]string, error) { return r.cols, nil }

func (r *d1Rows) Next() bool {
	if r.cur < r.rowsLen() {
		r.cur++
		return true
	}
	return false
}

func (r *d1Rows) Scan(dest ...any) error {
	if r.cur == 0 || r.cur > r.rowsLen() {
		return Err(errPrefix + "invalid row cursor")
	}
	row := r.arr.Index(r.cur - 1)
	if len(dest) != len(r.cols) {
		return Err(errPrefix + "scan destination count mismatch")
	}
	for i, ptr := range dest {
		if err := jsvalue.ScanValue(row.Index(i), ptr); err != nil {
			return err
		}
	}
	return nil
}

func (r *d1Rows) Close() error { return nil }
func (r *d1Rows) Err() error   { return nil }

type errScanner struct{ err error }

func (e *errScanner) Scan(...any) error { return e.err }

type rowScanner struct{ row js.Value }

func (s *rowScanner) Scan(dest ...any) error {
	if s.row.IsUndefined() || s.row.IsNull() {
		return Err(errPrefix + "no results to scan")
	}
	if len(dest) > s.row.Length() {
		return Err(errPrefix + "scan destination count mismatch")
	}
	for i, ptr := range dest {
		if err := jsvalue.ScanValue(s.row.Index(i), ptr); err != nil {
			return err
		}
	}
	return nil
}

// compile-time check
var _ storage.Rows = (*d1Rows)(nil)
