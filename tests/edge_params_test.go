//go:build wasm

package cloudflare_test

import (
	"testing"

	"github.com/tinywasm/cloudflare/edge"
	"github.com/tinywasm/model"
	"github.com/tinywasm/router"
)

func TestEdgeParamsAreNotContextValues(t *testing.T) {
	r := edge.NewRouter(edge.Config{})
	var paramVal string
	var ctxVal string

	r.Get("/api/items/{id}", func(ctx router.Context) {
		paramVal = ctx.Param("id")
		ctxVal = ctx.Value("id")
	}).Public()

	edge.Dispatch(r, &testCtx{method: "GET", path: "/api/items/7"})

	if paramVal != "7" {
		t.Fatalf("expected Param(\"id\") to be \"7\", got %q", paramVal)
	}
	if ctxVal != "" {
		t.Fatalf("expected Value(\"id\") to be \"\", got %q", ctxVal)
	}
}

func TestEdgeParamsResetPerRequest(t *testing.T) {
	r := edge.NewRouter(edge.Config{})
	var paramVal1, paramVal2 string

	r.Get("/api/items/{id}", func(ctx router.Context) {
		if paramVal1 == "" {
			paramVal1 = ctx.Param("id")
		} else {
			paramVal2 = ctx.Param("id")
		}
	}).Public()

	// Sequential requests using a shared context object to verify reset
	tc := &testCtx{method: "GET", path: "/api/items/42"}
	edge.Dispatch(r, tc)

	tc.path = "/api/items/99"
	edge.Dispatch(r, tc)

	if paramVal1 != "42" {
		t.Fatalf("expected first param to be \"42\", got %q", paramVal1)
	}
	if paramVal2 != "99" {
		t.Fatalf("expected second param to be \"99\", got %q", paramVal2)
	}
}

func TestEdgeRegistrationRejectsWildcard(t *testing.T) {
	defer func() {
		rec := recover()
		if rec == nil {
			t.Fatal("expected panic when registering wildcard route")
		}
	}()

	r := edge.NewRouter(edge.Config{})
	r.Get("/x/{a...}", func(ctx router.Context) {})
}

func TestEdgeLiteralBeatsParameterAcrossMethods(t *testing.T) {
	r := edge.NewRouter(edge.Config{})
	var reached string

	r.Get("/api/items/new", func(ctx router.Context) {
		reached = "literal"
	}).Public()

	r.Get("/api/items/{id}", func(ctx router.Context) {
		reached = "param"
	}).Public()

	edge.Dispatch(r, &testCtx{method: "GET", path: "/api/items/new"})

	if reached != "literal" {
		t.Fatalf("expected \"literal\" handler to be reached, got %q", reached)
	}
}

func TestEdgeSubtreeRouteStillMatches(t *testing.T) {
	r := edge.NewRouter(edge.Config{})
	var reached bool

	r.Get("/assets/", func(ctx router.Context) {
		reached = true
	}).Public()

	edge.Dispatch(r, &testCtx{method: "GET", path: "/assets/a/b/c"})

	if !reached {
		t.Fatal("expected prefix route /assets/ to match /assets/a/b/c")
	}
}

type testCtx struct {
	method      string
	path        string
	status      int
	paramNames  []string
	paramValues []string
}

func (c *testCtx) SetParams(names, values []string) {
	c.paramNames = names
	c.paramValues = values
}

func (c *testCtx) Param(name string) string {
	for i := 0; i < len(c.paramNames); i++ {
		if c.paramNames[i] == name {
			return c.paramValues[i]
		}
	}
	return ""
}

func (c *testCtx) Method() string                             { return c.method }
func (c *testCtx) Path() string                               { return c.path }
func (c *testCtx) Body() []byte                               { return nil }
func (c *testCtx) GetHeader(string) string                    { return "" }
func (c *testCtx) SetHeader(string, string)                   {}
func (c *testCtx) WriteStatus(code int)                       { c.status = code }
func (c *testCtx) Write(b []byte) (int, error)                { return len(b), nil }
func (c *testCtx) SetValue(string, string)                    {}
func (c *testCtx) Value(string) string                        { return "" }
func (c *testCtx) SetUserID(string)                           {}
func (c *testCtx) UserID() string                             { return "" }
func (c *testCtx) Decode(model.Decodable) error             { return nil }
func (c *testCtx) Encode(model.Encodable) error             { return nil }
func (c *testCtx) SetCookie(router.Cookie)                    {}
func (c *testCtx) Cookie(string) (router.Cookie, bool)        { return router.Cookie{}, false }

var _ router.Context = (*testCtx)(nil)
