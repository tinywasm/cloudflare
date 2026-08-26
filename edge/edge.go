//go:build wasm

package edge

import (
	"syscall/js"

	"github.com/tinywasm/cloudflare/d1"
	"github.com/tinywasm/cloudflare/log"
	"github.com/tinywasm/cloudflare/workers"
	"github.com/tinywasm/context"
	"github.com/tinywasm/fmt"
	"github.com/tinywasm/json"
	"github.com/tinywasm/model"
	"github.com/tinywasm/router"
)

const bookmarkKey = "d1_bookmark"

type edgeBookmarkStore struct {
	ctx router.Context
}

func (s *edgeBookmarkStore) Bookmark() string {
	return s.ctx.Value(bookmarkKey)
}

func (s *edgeBookmarkStore) SetBookmark(bm string) {
	s.ctx.SetValue(bookmarkKey, bm)
}

// BookmarkStore returns a d1.BookmarkStore backed by ctx's per-request string
// storage. It is the link between the router and the D1 adapter: every request
// carries its own, so two concurrent requests in the same isolate never share a
// database version.
func BookmarkStore(ctx router.Context) d1.BookmarkStore {
	return &edgeBookmarkStore{ctx: ctx}
}

type wasmContext struct {
	req  *workers.Request
	res  *workers.Response
	path string
	vals *context.Context
	uid  string
}

func (c *wasmContext) Method() string { return c.req.Method }
func (c *wasmContext) Path() string   { return c.path }
func (c *wasmContext) Body() []byte   { return c.req.Body() }
func (c *wasmContext) GetHeader(key string) string {
	return c.req.Header(key)
}
func (c *wasmContext) SetHeader(key, value string) {
	c.res.SetHeader(key, value)
}
func (c *wasmContext) WriteStatus(code int) {
	c.res.WriteHeader(code)
}
func (c *wasmContext) Write(b []byte) (int, error) {
	return c.res.Write(b)
}

// SetValue stores a request-scoped value. tinywasm/context holds string
// values only (no maps — fixed 16-pair array, TinyGo-friendly) and router.Context
// is typed accordingly, so there is no non-string case left to guard against.
// The remaining panic is the 16-pair capacity limit: a wiring mistake (too
// many SetValue-calling middlewares on one request), deterministic given the
// app's own middleware stack and caught by the first test that exercises it
// — not a per-request data condition, so it stays a panic rather than an
// error every caller would have to remember to check.
func (c *wasmContext) SetValue(key, value string) {
	if c.vals == nil {
		c.vals = context.Background()
	}
	if err := c.vals.Set(key, value); err != nil {
		panic(fmt.Sprintf("edge: SetValue(%q): %v", key, err))
	}
}

func (c *wasmContext) Value(key string) string {
	return c.vals.Value(key)
}

func (c *wasmContext) SetUserID(id string) {
	c.uid = id
}

func (c *wasmContext) UserID() string {
	return c.uid
}

func (c *wasmContext) Decode(into model.Decodable) error {
	return json.Decode(c.Body(), into)
}

func (c *wasmContext) Encode(v model.Encodable) error {
	var buf []byte
	if err := json.Encode(v, &buf); err != nil {
		return err
	}
	_, err := c.Write(buf)
	return err
}

func (c *wasmContext) SetCookie(cookie router.Cookie) {
	var s string
	s = fmt.Sprintf("%s=%s; Path=%s", cookie.Name, cookie.Value, cookie.Path)
	if cookie.Domain != "" {
		s += "; Domain=" + cookie.Domain
	}
	if cookie.MaxAge > 0 {
		s += fmt.Sprintf("; Max-Age=%d", cookie.MaxAge)
	}
	if cookie.Secure {
		s += "; Secure"
	}
	if cookie.HttpOnly {
		s += "; HttpOnly"
	}
	switch cookie.SameSite {
	case router.SameSiteLax:
		s += "; SameSite=Lax"
	case router.SameSiteStrict:
		s += "; SameSite=Strict"
	case router.SameSiteNone:
		s += "; SameSite=None"
	}

	existing := c.res.GetHeader("Set-Cookie")
	if existing == "" {
		c.res.SetHeader("Set-Cookie", s)
	} else {
		c.res.SetHeader("Set-Cookie", existing+", "+s)
	}
}

func (c *wasmContext) Cookie(name string) (router.Cookie, bool) {
	h := c.req.Header("Cookie")
	if h == "" {
		return router.Cookie{}, false
	}

	// Manual parsing of "Cookie" header: name1=val1; name2=val2
	// Minimal implementation: search for "name="
	// Note: we can't use strings.Split (stdlib prohibited)
	// We'll do a simple scan directly over the string — Go strings already
	// index by byte, so copying to []byte first only allocated a throwaway
	// copy for no benefit.
	needle := name + "="
	for i := 0; i <= len(h)-len(needle); i++ {
		if h[i:i+len(needle)] != needle {
			continue
		}
		if i != 0 && h[i-1] != ' ' && h[i-1] != ';' {
			continue
		}
		start := i + len(needle)
		end := start
		for end < len(h) && h[end] != ';' {
			end++
		}
		return router.Cookie{Name: name, Value: h[start:end]}, true
	}

	return router.Cookie{}, false
}

type wasmRoute struct {
	info    router.RouteInfo
	h       router.HandlerFunc
	wrapped router.HandlerFunc // set once by compile(), never per request
}

func (r *wasmRoute) Requires(resource model.Resource, action model.Action) router.Route {
	r.info.Access = model.AccessGuarded
	r.info.Resource = resource
	r.info.Action = action
	return r
}

func (r *wasmRoute) Authenticated() router.Route {
	r.info.Access = model.AccessAuthenticated
	return r
}

func (r *wasmRoute) Public() router.Route {
	r.info.Access = model.AccessPublic
	return r
}

func (r *wasmRoute) Accepts(args model.Fielder) router.Route {
	r.info.Args = args
	return r
}

// Config declares WHO the caller is and WHAT they may do. The library supplies the
// mechanism; the policy belongs to the app.
//
// The zero value is legal — an app with no authentication — and its public routes work.
// What it cannot do is mount a guarded route without saying who authorizes it: Serve
// refuses to start on that contradiction, instead of answering 403 forever in silence.
type Config struct {
	// Authn establishes identity. It runs BEFORE the access gate, and that ordering is the
	// whole point: a gate that runs first can never be satisfied, so every guarded route
	// becomes a permanent 403. That is exactly the bug this replaced — it made the file
	// upload API unusable in production while the tests stayed green.
	//
	// It reads the request (cookie, header, token) and calls ctx.SetUserID. Anonymous ("")
	// is a legal outcome, not an error.
	Authn router.Middleware

	// Authorize answers whether that identity holds a permission. nil DENIES: the absence
	// of an answer is not permission.
	Authorize model.Authorizer
}

type wasmRouter struct {
	cfg         Config
	routes      []*wasmRoute
	middlewares []router.Middleware
}

// NewRouter builds the edge router. It takes a Config on purpose: the no-argument version
// could not authenticate anybody, which made every guarded route unreachable. An app with
// no auth passes edge.Config{} — explicitly.
func NewRouter(cfg Config) router.Router {
	return &wasmRouter{cfg: cfg}
}

func (r *wasmRouter) Get(path string, h router.HandlerFunc) router.Route {
	return r.Handle("GET", path, h)
}
func (r *wasmRouter) Post(path string, h router.HandlerFunc) router.Route {
	return r.Handle("POST", path, h)
}
func (r *wasmRouter) Put(path string, h router.HandlerFunc) router.Route {
	return r.Handle("PUT", path, h)
}
func (r *wasmRouter) Delete(path string, h router.HandlerFunc) router.Route {
	return r.Handle("DELETE", path, h)
}
func (r *wasmRouter) Options(path string, h router.HandlerFunc) router.Route {
	return r.Handle("OPTIONS", path, h)
}
func (r *wasmRouter) Handle(method, path string, h router.HandlerFunc) router.Route {
	rt := &wasmRoute{
		info: router.RouteInfo{Method: method, Path: path},
		h:    h,
	}
	r.routes = append(r.routes, rt)
	return rt
}

// PublicAsset registra UNA ruta que sirve UN archivo al navegador.
func (r *wasmRouter) PublicAsset(path string, h router.HandlerFunc) {
	route := &wasmRoute{
		info: router.RouteInfo{Method: "GET", Path: path, Access: model.AccessPublic},
		h:    h,
	}
	r.routes = append(r.routes, route)
}

// PublicDir sirve un directorio bajo un prefijo. Mismo contrato.
func (r *wasmRouter) PublicDir(prefix string, dir string) {
	route := &wasmRoute{
		info: router.RouteInfo{Method: "GET", Path: prefix, Access: model.AccessPublic, Dir: dir},
	}
	r.routes = append(r.routes, route)
}

func (r *wasmRouter) Use(m ...router.Middleware) {
	r.middlewares = append(r.middlewares, m...)
}

func (r *wasmRouter) Routes() []router.RouteInfo {
	infos := make([]router.RouteInfo, len(r.routes))
	for i, rt := range r.routes {
		infos[i] = rt.info
	}
	return infos
}

func (r *wasmRouter) Stream(path string, h router.StreamFunc) router.Route {
	panic("Stream not supported in this runtime")
}

func (r *wasmRouter) Socket(path string, h router.SocketFunc) router.Route {
	panic("Socket not supported in this runtime")
}

// pathMatches reports whether pathname is served by pattern. A pattern ending in "/" matches
// by prefix (that is how files.Store hangs a generated key off /api/files/); anything else is
// an exact match.
func pathMatches(pattern, pathname string) bool {
	if pattern == "" {
		return false
	}
	if pattern[len(pattern)-1] != '/' {
		return pathname == pattern
	}
	if len(pathname) < len(pattern) {
		return false
	}
	for i := 0; i < len(pattern); i++ {
		if pathname[i] != pattern[i] {
			return false
		}
	}
	return true
}

// match finds the route for a method+path. The longest matching path wins, so a specific
// route beats a prefix one. A route registered with an empty method matches any method.
//
// The second result is the status to answer when nothing matched: 405 when the path exists
// but not for this method, 404 when it does not exist at all.
func (r *wasmRouter) match(method, pathname string) (*wasmRoute, int) {
	var best *wasmRoute
	pathExists := false

	for _, rt := range r.routes {
		if !pathMatches(rt.info.Path, pathname) {
			continue
		}
		pathExists = true
		if rt.info.Method != "" && rt.info.Method != method {
			continue
		}
		if best == nil || len(rt.info.Path) > len(best.info.Path) {
			best = rt
		}
	}

	if best != nil {
		return best, 200
	}
	if pathExists {
		return nil, 405
	}
	return nil, 404
}

// allows is the access gate. The zero value of Access is AccessGuarded, so a route that
// declares nothing is unreachable — and validateRoutes already refused to start on it.
func (r *wasmRouter) allows(info router.RouteInfo, userID string) (bool, string) {
	switch info.Access {
	case model.AccessPublic:
		return true, ""
	case model.AccessAuthenticated:
		if userID == "" {
			return false, "anonymous caller on a route that requires an identity"
		}
		return true, ""
	default: // model.AccessGuarded
		if userID == "" {
			return false, "anonymous caller on a route requiring " + string(info.Resource)
		}
		// model.Allowed denies when Authorize is nil: the absence of an answer is not
		// permission.
		if !model.Allowed(r.cfg.Authorize, userID, info.Resource, info.Action) {
			return false, "identity lacks " + info.Action.String() + " on " + string(info.Resource)
		}
		return true, ""
	}
}

// Validate refuses to start on a contradiction. Each of these denies EVERY caller, forever,
// on a route that LOOKS protected — and the only way to discover that is a 403 in production,
// which is exactly how the file upload API shipped unusable.
//
// It panics rather than returning an error: there is nobody to hand an error to at the top of
// a Worker, and goflare recovers and logs panics. Loud beats silent.
func Validate(r router.Router) {
	wr := r.(*wasmRouter)
	for _, rt := range wr.routes {
		if rt.info.Access != model.AccessGuarded {
			continue
		}
		if rt.info.Resource == "" {
			panic("edge: route " + rt.info.Method + " " + rt.info.Path +
				" is guarded but declares no resource: it is unreachable")
		}
		if wr.cfg.Authorize == nil {
			panic("edge: route " + rt.info.Method + " " + rt.info.Path +
				" requires resource \"" + string(rt.info.Resource) +
				"\" but no Authorize is configured: it would deny every caller")
		}
		if wr.cfg.Authn == nil {
			panic("edge: route " + rt.info.Method + " " + rt.info.Path +
				" needs an identity but no Authn is configured: no caller can ever be authorized")
		}
	}
}

// compile wraps every route's handler with the middleware chain exactly
// once. gateAndServe used to do this on every request — a closure
// allocation per middleware, per request — even though neither a route's
// handler nor r.middlewares ever changes after Serve() starts. Now there is
// nothing left to rebuild: dispatch calls the frozen wrapped handler
// directly.
func (r *wasmRouter) compile() {
	for _, rt := range r.routes {
		if rt.h == nil {
			continue // static file / directory routes are served elsewhere
		}
		h := rt.h
		for i := len(r.middlewares) - 1; i >= 0; i-- {
			h = r.middlewares[i](h)
		}
		rt.wrapped = h
	}
}

// ExportCompile exports compile for testing.
func ExportCompile(r router.Router) {
	r.(*wasmRouter).compile()
}

func Serve(r router.Router) {
	wr := r.(*wasmRouter)

	// Loudly, at startup — never a silent 403 in production.
	Validate(wr)
	wr.compile()

	workers.Handle(func(res *workers.Response, req *workers.Request) {
		pathname := js.Global().Get("URL").New(req.URL).Get("pathname").String()
		Dispatch(wr, &wasmContext{req: req, res: res, path: pathname})
	})
}

// Dispatch drives ONE request through the full pipeline: identity, access gate, middleware,
// handler. It speaks only router.Context, so the pipeline that runs in production is the
// same one a test can drive — with no Cloudflare runtime and no js.Global() in sight.
//
// That is not a convenience: the previous tests called the matched handler DIRECTLY, past
// the gate, which is why they stayed green while every guarded route answered 403 in
// production. A pipeline you cannot drive is a pipeline nobody tests.
func Dispatch(r router.Router, ctx router.Context) {
	wr := r.(*wasmRouter)

	// The gate decides with the identity Authn established. Run these the other way round —
	// as this router used to — and no caller can EVER be authorized, on any route: the gate
	// reads a UserID that nothing has written yet.
	gate := func(c router.Context) { wr.gateAndServe(c) }
	if wr.cfg.Authn != nil {
		gate = wr.cfg.Authn(gate)
	}
	gate(ctx)
}

func (r *wasmRouter) gateAndServe(ctx router.Context) {
	method, pathname := ctx.Method(), ctx.Path()

	route, status := r.match(method, pathname)
	if route == nil {
		reason := "no route matches"
		if status == 405 {
			reason = "the path exists but not for this method"
		}
		log.Reject(status, method, pathname, reason)
		ctx.WriteStatus(status)
		ctx.Write([]byte(fmt.Convert(status).String()))
		return
	}

	if ok, why := r.allows(route.info, ctx.UserID()); !ok {
		log.Reject(403, method, pathname, why)
		ctx.WriteStatus(403)
		ctx.Write([]byte("Forbidden"))
		return
	}

	// Middleware runs BEHIND the gate: a rejected request must not execute the consumer's
	// logic — decoding a body or hitting a database for a caller about to get a 403 is work
	// (and attack surface) handed to somebody already denied.
	if route.wrapped != nil {
		route.wrapped(ctx)
	} else if route.h != nil {
		route.h(ctx)
	}
}

var _ router.Router = (*wasmRouter)(nil)
var _ router.Context = (*wasmContext)(nil)
var _ router.Route = (*wasmRoute)(nil)
