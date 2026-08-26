//go:build wasm

package d1

import (
	"syscall/js"

	"github.com/tinywasm/orm"
	"github.com/tinywasm/sqlt"
)

const (
	// BookmarkFirstUnconstrained abre la sesion contra cualquier instancia,
	// replica incluida. Es lo mas rapido y lo correcto para lecturas que
	// toleran el retraso de replicacion.
	BookmarkFirstUnconstrained = "first-unconstrained"

	// BookmarkFirstPrimary fuerza la primera consulta contra la primaria. Es
	// lo correcto cuando la peticion va a escribir, o cuando debe ver una
	// escritura hecha en otra peticion sin bookmark que las enlace.
	BookmarkFirstPrimary = "first-primary"
)

// BookmarkStore lleva el bookmark de D1 de una sentencia a la siguiente dentro
// de una misma peticion. La implementacion la inyecta el consumidor y DEBE ser
// por peticion: varias peticiones pueden estar en vuelo a la vez en un mismo
// isolate mientras esperan promesas, asi que un almacen compartido entre ellas
// mezclaria versiones de la base entre usuarios distintos.
type BookmarkStore interface {
	Bookmark() string
	SetBookmark(string)
}

// NewEdgeSession abre el binding igual que NewEdge, pero enruta cada sentencia
// por la Sessions API de D1 usando store para encadenar bookmarks. Sin esto,
// las replicas de lectura no se usan aunque esten activadas.
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
