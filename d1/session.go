//go:build wasm

package d1

import (
	"syscall/js"

	"github.com/tinywasm/orm"
	"github.com/tinywasm/sqlt"
)

const (
	// BookmarkFirstUnconstrained opens the session against any instance,
	// replicas included. It is the fastest option and the right one for reads
	// that tolerate replication lag.
	BookmarkFirstUnconstrained = "first-unconstrained"

	// BookmarkFirstPrimary forces the first query onto the primary. It is the
	// right one when the request is going to write, or when it must see a write
	// made by another request with no bookmark linking the two.
	BookmarkFirstPrimary = "first-primary"
)

// BookmarkStore carries D1's bookmark from one statement to the next within a
// single request. The consumer injects the implementation, and it MUST be
// per-request: several requests can be in flight at once inside one isolate
// while they await promises, so a store shared between them would mix database
// versions across different users.
type BookmarkStore interface {
	Bookmark() string
	SetBookmark(string)
}

// NewEdgeSession opens the binding just like NewEdge, but routes every
// statement through D1's Sessions API, using store to chain bookmarks. Without
// this, read replicas go unused even when they are enabled.
func NewEdgeSession(bindingName string, store BookmarkStore) (*orm.DB, error) {
	v := js.Global().Get("context").Get("env").Get(bindingName)
	if v.IsUndefined() || v.IsNull() {
		return nil, ErrDatabaseNotFound
	}
	return orm.New(&adapter{
		dbObj:    v,
		store:    store,
		Compiler: sqlt.NewCompiler(),
	}), nil
}
