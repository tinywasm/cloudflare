//go:build wasm

package cloudflare_test

import (
	"testing"

	"github.com/tinywasm/cloudflare/edge"
	"github.com/tinywasm/json"
	"github.com/tinywasm/model"
	"github.com/tinywasm/router"
	"github.com/tinywasm/router/conformance"
)

// conformanceUserHeader is the seam this test authenticates through. A deployed Worker reads
// a cookie or a bearer token instead; the shape is identical, which is the point.
const conformanceUserHeader = "X-Conformance-User"

// conformanceCtx is a router.Context that records what the pipeline did to it.
//
// Note what it does NOT do: it does not stub SetUserID into a no-op, and it does not let a
// test call a handler directly. Those two shortcuts are exactly why this package's earlier
// fakes passed while the file upload API answered 403 to every caller in production. A fake
// that cannot fail is not a test, it is a blindfold.
type conformanceCtx struct {
	method  string
	path    string
	body    []byte
	headers map[string]string
	uid     string

	status int
	out    []byte
}

func (c *conformanceCtx) Method() string { return c.method }
func (c *conformanceCtx) Path() string   { return c.path }
func (c *conformanceCtx) Body() []byte   { return c.body }
func (c *conformanceCtx) GetHeader(k string) string {
	return c.headers[k]
}
func (c *conformanceCtx) SetHeader(k, v string) {
	if c.headers == nil {
		c.headers = map[string]string{}
	}
	c.headers[k] = v
}
func (c *conformanceCtx) WriteStatus(code int) { c.status = code }
func (c *conformanceCtx) Write(b []byte) (int, error) {
	c.out = append(c.out, b...)
	return len(b), nil
}
func (c *conformanceCtx) SetValue(string, any)    {}
func (c *conformanceCtx) Value(string) any        { return nil }
func (c *conformanceCtx) SetCookie(router.Cookie) {}
func (c *conformanceCtx) Cookie(string) (router.Cookie, bool) {
	return router.Cookie{}, false
}

// SetUserID records the identity. It used to be an empty method — which meant the gate could
// never see a caller, and no test could ever notice.
func (c *conformanceCtx) SetUserID(id string) { c.uid = id }
func (c *conformanceCtx) UserID() string      { return c.uid }

func (c *conformanceCtx) Decode(into model.Decodable) error {
	return json.Decode(c.Body(), into)
}

func (c *conformanceCtx) Encode(v model.Encodable) error {
	var buf []byte
	if err := json.Encode(v, &buf); err != nil {
		return err
	}
	_, err := c.Write(buf)
	return err
}

var _ router.Context = (*conformanceCtx)(nil)

// TestEdgeConformance holds the edge router to the shared contract — the same suite
// server/httpd passes. It is what turns "these two implementations agree" from folklore into
// something that goes red.
func TestEdgeConformance(t *testing.T) {
	conformance.Run(t, conformance.Factory{
		New: func(t *testing.T, s conformance.Setup) (router.Router, conformance.ServeFunc) {
			r := edge.NewRouter(edge.Config{
				Authorize: s.Authorize,
				Authn: func(next router.HandlerFunc) router.HandlerFunc {
					return func(ctx router.Context) {
						if id := ctx.GetHeader(conformanceUserHeader); id != "" {
							ctx.SetUserID(id)
						}
						next(ctx)
					}
				},
			})

			serve := func(method, path string, body []byte, userID string) conformance.Response {
				ctx := &conformanceCtx{method: method, path: path, body: body}
				if userID != "" {
					ctx.SetHeader(conformanceUserHeader, userID)
				}
				edge.Dispatch(r, ctx)
				return conformance.Response{Status: ctx.status, Body: ctx.out}
			}
			return r, serve
		},

		// The edge fails at startup by panicking: on a Worker there is no error to return to,
		// and a panic is logged and recovered. Either way it is LOUD — which a permanent 403
		// never was.
		Verify: func(r router.Router) (err error) {
			defer func() {
				if rec := recover(); rec != nil {
					err = errStartup{rec}
				}
			}()
			edge.Validate(r)
			return nil
		},
	})
}

type errStartup struct{ v any }

func (e errStartup) Error() string {
	if s, ok := e.v.(string); ok {
		return s
	}
	return "edge refused to start"
}
