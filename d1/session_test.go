//go:build wasm

package d1

import (
	"syscall/js"
	"testing"
)

type mockStore struct {
	bm string
}

func (m *mockStore) Bookmark() string {
	return m.bm
}

func (m *mockStore) SetBookmark(bm string) {
	m.bm = bm
}

func setupMockDB(withSessionFunc js.Value, prepareFunc js.Value) js.Value {
	obj := js.Global().Get("Object").New()
	if !withSessionFunc.IsUndefined() && !withSessionFunc.IsNull() {
		obj.Set("withSession", withSessionFunc)
	}
	if !prepareFunc.IsUndefined() && !prepareFunc.IsNull() {
		obj.Set("prepare", prepareFunc)
	}

	env := js.Global().Get("Object").New()
	env.Set("DB", obj)

	ctx := js.Global().Get("Object").New()
	ctx.Set("env", env)

	js.Global().Set("context", ctx)
	return obj
}

// fakePromise returns a real JS promise already resolved with val. Using
// Promise.resolve instead of a hand-rolled object with a then() is deliberate:
// await.Promise chains .then(...).catch(...), so a double without catch makes
// syscall/js panic before any assertion is reached.
func fakePromise(val js.Value) js.Value {
	return js.Global().Get("Promise").Call("resolve", val)
}

func TestNewEdgeDoesNotUseSessions(t *testing.T) {
	withSessionCalled := false
	prepareCalled := false

	withSessionFunc := js.FuncOf(func(this js.Value, args []js.Value) any {
		withSessionCalled = true
		return js.Undefined()
	})
	defer withSessionFunc.Release()

	prepareFunc := js.FuncOf(func(this js.Value, args []js.Value) any {
		prepareCalled = true

		stmt := js.Global().Get("Object").New()
		stmt.Set("bind", js.FuncOf(func(this js.Value, args []js.Value) any {
			return stmt
		}))

		stmt.Set("run", js.FuncOf(func(this js.Value, args []js.Value) any {
			return fakePromise(js.Undefined())
		}))
		return stmt
	})
	defer prepareFunc.Release()

	setupMockDB(js.ValueOf(withSessionFunc), js.ValueOf(prepareFunc))

	db, err := NewEdge("DB")
	if err != nil {
		t.Fatalf("NewEdge failed: %v", err)
	}

	adapterObj := db.RawConn().(*adapter)
	err = adapterObj.Exec("SELECT 1")
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}

	if withSessionCalled {
		t.Errorf("expected withSession NOT to be called, but it was")
	}
	if !prepareCalled {
		t.Errorf("expected prepare to be called, but it was not")
	}
}

func TestSessionChainsBookmarks(t *testing.T) {
	var capturedBookmarks []string

	prepareFunc := js.FuncOf(func(this js.Value, args []js.Value) any {
		stmt := js.Global().Get("Object").New()
		stmt.Set("bind", js.FuncOf(func(this js.Value, args []js.Value) any {
			return stmt
		}))
		stmt.Set("raw", js.FuncOf(func(this js.Value, args []js.Value) any {
			arr := js.Global().Get("Array").New()
			cols := js.Global().Get("Array").New()
			cols.Call("push", "id")
			row := js.Global().Get("Array").New()
			row.Call("push", 1)
			arr.Call("push", cols)
			arr.Call("push", row)
			return fakePromise(arr)
		}))
		return stmt
	})
	defer prepareFunc.Release()

	sessIndex := 0
	withSessionFunc := js.FuncOf(func(this js.Value, args []js.Value) any {
		bm := args[0].String()
		capturedBookmarks = append(capturedBookmarks, bm)

		sess := js.Global().Get("Object").New()
		sess.Set("prepare", prepareFunc)

		sessIndex++
		curIdx := sessIndex
		sess.Set("getBookmark", js.FuncOf(func(this js.Value, args []js.Value) any {
			if curIdx == 1 {
				return "bm-123"
			}
			return "bm-456"
		}))
		return sess
	})
	defer withSessionFunc.Release()

	setupMockDB(js.ValueOf(withSessionFunc), js.Undefined())

	store := &mockStore{}
	db, err := NewEdgeSession("DB", store)
	if err != nil {
		t.Fatalf("NewEdgeSession failed: %v", err)
	}

	adapterObj := db.RawConn().(*adapter)
	rows, err := adapterObj.Query("SELECT 1")
	if err != nil {
		t.Fatalf("Query 1 failed: %v", err)
	}
	rows.Close()

	if store.Bookmark() != "bm-123" {
		t.Errorf("expected store bookmark 'bm-123', got %q", store.Bookmark())
	}

	rows2, err := adapterObj.Query("SELECT 2")
	if err != nil {
		t.Fatalf("Query 2 failed: %v", err)
	}
	rows2.Close()

	if store.Bookmark() != "bm-456" {
		t.Errorf("expected store bookmark 'bm-456', got %q", store.Bookmark())
	}

	if len(capturedBookmarks) != 2 {
		t.Fatalf("expected 2 captured bookmarks in withSession, got %d", len(capturedBookmarks))
	}
	if capturedBookmarks[0] != BookmarkFirstUnconstrained {
		t.Errorf("expected first call to receive BookmarkFirstUnconstrained, got %q", capturedBookmarks[0])
	}
	if capturedBookmarks[1] != "bm-123" {
		t.Errorf("expected second call to receive 'bm-123', got %q", capturedBookmarks[1])
	}
}

func TestNullBookmarkIsIgnored(t *testing.T) {
	prepareFunc := js.FuncOf(func(this js.Value, args []js.Value) any {
		stmt := js.Global().Get("Object").New()
		stmt.Set("bind", js.FuncOf(func(this js.Value, args []js.Value) any {
			return stmt
		}))
		stmt.Set("run", js.FuncOf(func(this js.Value, args []js.Value) any {
			return fakePromise(js.Undefined())
		}))
		return stmt
	})
	defer prepareFunc.Release()

	withSessionFunc := js.FuncOf(func(this js.Value, args []js.Value) any {
		sess := js.Global().Get("Object").New()
		sess.Set("prepare", prepareFunc)
		sess.Set("getBookmark", js.FuncOf(func(this js.Value, args []js.Value) any {
			return js.Null()
		}))
		return sess
	})
	defer withSessionFunc.Release()

	setupMockDB(js.ValueOf(withSessionFunc), js.Undefined())

	store := &mockStore{bm: "initial-bm"}
	db, err := NewEdgeSession("DB", store)
	if err != nil {
		t.Fatalf("NewEdgeSession failed: %v", err)
	}

	adapterObj := db.RawConn().(*adapter)
	err = adapterObj.Exec("UPDATE foo SET bar = 1")
	if err != nil {
		t.Fatalf("Exec failed: %v", err)
	}

	if store.Bookmark() != "initial-bm" {
		t.Errorf("expected store bookmark to stay 'initial-bm', got %q", store.Bookmark())
	}
}
