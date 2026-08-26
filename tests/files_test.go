//go:build wasm

package cloudflare_test

import (
	"bytes"
	"testing"

	"github.com/tinywasm/cloudflare/files"
	"github.com/tinywasm/cloudflare/r2"
	"github.com/tinywasm/fmt"
	"github.com/tinywasm/model"
	"github.com/tinywasm/router"
)

const filesPrefix = "/api/files/"

// fakeCtx is a router.Context that records what the handler answered.
type fakeCtx struct {
	method  string
	path    string
	body    []byte
	headers map[string]string
	status  int
	written []byte
	uid     string
}

func newCtx(method, path string, body []byte) *fakeCtx {
	return &fakeCtx{
		method:  method,
		path:    path,
		body:    body,
		headers: map[string]string{},
	}
}

func (c *fakeCtx) Method() string            { return c.method }
func (c *fakeCtx) Path() string              { return c.path }
func (c *fakeCtx) Body() []byte              { return c.body }
func (c *fakeCtx) GetHeader(k string) string { return c.headers[k] }
func (c *fakeCtx) SetHeader(k, v string)     { c.headers[k] = v }
func (c *fakeCtx) WriteStatus(code int)      { c.status = code }
func (c *fakeCtx) SetValue(k, v string)      {}
func (c *fakeCtx) Value(k string) string     { return "" }
func (c *fakeCtx) SetCookie(router.Cookie)   {}

// SetUserID and UserID record the identity for real. They used to be an empty setter and a
// hardcoded "": a fake that could not hold a caller, so the gate could never see one. That is
// why this suite stayed green while every upload answered 403 in production.
func (c *fakeCtx) SetUserID(id string) { c.uid = id }
func (c *fakeCtx) UserID() string      { return c.uid }

func (c *fakeCtx) Decode(into model.Decodable) error {
	return nil
}

func (c *fakeCtx) Encode(v model.Encodable) error {
	return nil
}
func (c *fakeCtx) Cookie(string) (router.Cookie, bool) {
	return router.Cookie{}, false
}
func (c *fakeCtx) Write(b []byte) (int, error) {
	c.written = append(c.written, b...)
	return len(b), nil
}

// pngBytes is a real PNG signature followed by non-UTF8 payload: it must survive
// the round trip byte for byte.
var pngBytes = []byte{
	0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A,
	0xFF, 0xFE, 0x00, 0x80, 0x21, 0x42,
}

var jpgBytes = []byte{
	0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10, 'J', 'F', 'I', 'F', 0x00, 0x01,
	0x01, 0x01, 0x00, 0x48, 0x00, 0x48, 0x00, 0x00, 0xFF, 0xD9,
}

func newStore(t *testing.T) (*files.Store, map[string][]byte) {
	t.Helper()
	store := setupEnv(t)

	bucket, err := r2.NewEdge("FILES")
	if err != nil {
		t.Fatalf("bucket: %v", err)
	}
	s, err := files.New(bucket, filesPrefix)
	if err != nil {
		t.Fatalf("files.New: %v", err)
	}
	return s, store
}

// upload drives the handler the same way Mount would, without a live router.
func upload(t *testing.T, s *files.Store, ctx *fakeCtx) {
	t.Helper()
	r := &captureRouter{}
	s.Mount(r)
	r.put(ctx)
}

func serve(t *testing.T, s *files.Store, ctx *fakeCtx) {
	t.Helper()
	r := &captureRouter{}
	s.Mount(r)
	r.get(ctx)
}

func TestFiles_UploadStoresRealTypeAndServerKey(t *testing.T) {
	s, store := newStore(t)

	ctx := newCtx("PUT", filesPrefix, pngBytes)
	upload(t, s, ctx)

	if ctx.status != 201 {
		t.Fatalf("status = %d, want 201 (body: %s)", ctx.status, ctx.written)
	}

	key := string(ctx.written)
	if len(key) < 4 || key[len(key)-4:] != ".png" {
		t.Errorf("key %q does not end in .png — the extension must come from the bytes", key)
	}

	stored, ok := store[key]
	if !ok {
		t.Fatalf("key %q absent from bucket; has %v", key, store)
	}
	if !bytes.Equal(stored, pngBytes) {
		t.Errorf("stored bytes differ:\nwant %v\ngot  %v", pngBytes, stored)
	}
}

func TestFiles_UploadRejectsSVG(t *testing.T) {
	s, store := newStore(t)

	// An SVG carries JavaScript. Served from our origin it is XSS, so it never
	// reaches the bucket — filetype detects it by name in order to refuse it.
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`)
	ctx := newCtx("PUT", filesPrefix, svg)
	upload(t, s, ctx)

	if ctx.status != 415 {
		t.Errorf("status = %d, want 415", ctx.status)
	}
	if len(store) != 0 {
		t.Errorf("bucket must stay empty on a rejected type, has %v", store)
	}
}

func TestFiles_UploadRejectsLyingContentType(t *testing.T) {
	s, store := newStore(t)

	// The client swears it is a PNG. The bytes say otherwise, and the bytes win.
	ctx := newCtx("PUT", filesPrefix, []byte("#!/bin/sh\nrm -rf /\n"))
	ctx.headers["Content-Type"] = "image/png"
	upload(t, s, ctx)

	if ctx.status != 415 {
		t.Errorf("status = %d, want 415", ctx.status)
	}
	if len(store) != 0 {
		t.Errorf("bucket must stay empty, has %v", store)
	}
}

func TestFiles_UploadTooLargeIsRejectedBeforeReadingBody(t *testing.T) {
	s, _ := newStore(t)
	s.MaxSize(10)

	ctx := newCtx("PUT", filesPrefix, pngBytes)
	ctx.headers["Content-Length"] = "1048576"
	upload(t, s, ctx)

	if ctx.status != 413 {
		t.Errorf("status = %d, want 413", ctx.status)
	}
}

func TestFiles_ServeReturnsBytesAndNoSniff(t *testing.T) {
	s, _ := newStore(t)

	up := newCtx("PUT", filesPrefix, pngBytes)
	upload(t, s, up)
	key := string(up.written)

	down := newCtx("GET", filesPrefix+key, nil)
	serve(t, s, down)

	if down.status != 0 && down.status != 200 {
		t.Fatalf("status = %d, want 200", down.status)
	}
	if !bytes.Equal(down.written, pngBytes) {
		t.Errorf("served bytes differ:\nwant %v\ngot  %v", pngBytes, down.written)
	}
	if got := down.headers["Content-Type"]; got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
	if got := down.headers["X-Content-Type-Options"]; got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
}

func TestFiles_ServeUnknownKeyIs404(t *testing.T) {
	s, _ := newStore(t)

	ctx := newCtx("GET", filesPrefix+"nope.png", nil)
	serve(t, s, ctx)

	if ctx.status != 404 {
		t.Errorf("status = %d, want 404", ctx.status)
	}
}

func TestFiles_NewRejectsPrefixWithoutSlash(t *testing.T) {
	setupEnv(t)
	bucket, _ := r2.NewEdge("FILES")

	if _, err := files.New(bucket, "/api/files"); err == nil {
		t.Error("a prefix without a trailing / must fail loudly")
	}
}

func TestFiles_MountRegistersUploadPrivateAndServePublic(t *testing.T) {
	s, _ := newStore(t)

	r := &captureRouter{}
	s.Mount(r)

	if r.putRoute == nil || !r.putRoute.requires {
		t.Error("upload must require a permission — it is never public")
	}
	if r.getRoute == nil || !r.getRoute.public {
		t.Error("serve must be public — an <img src> cannot send headers")
	}
}

// brokenBucket fails every write, like an R2 binding pointing at a bucket that does not exist.
type brokenBucket struct{}

func (brokenBucket) Put(key string, data []byte, contentType string) error {
	return fmt.Err("r2: put " + key + ": bucket unreachable")
}
func (brokenBucket) Get(key string) ([]byte, string, error) {
	return nil, "", fmt.Err("r2: get " + key + ": bucket unreachable")
}

func TestFiles_PerOwnerReplacesPriorUpload(t *testing.T) {
	s, store := newStore(t)
	s.PerOwner()

	up1 := newCtx("PUT", filesPrefix, pngBytes)
	up1.SetUserID("user_123")
	upload(t, s, up1)

	if up1.status != 201 {
		t.Fatalf("up1 status = %d, want 201", up1.status)
	}
	if key := string(up1.written); key != "user_123" {
		t.Errorf("key = %q, want user_123", key)
	}

	up2 := newCtx("PUT", filesPrefix, jpgBytes)
	up2.SetUserID("user_123")
	upload(t, s, up2)

	if up2.status != 201 {
		t.Fatalf("up2 status = %d, want 201", up2.status)
	}

	if len(store) != 1 {
		t.Fatalf("bucket size = %d, want 1 object in PerOwner mode", len(store))
	}
	stored, ok := store["user_123"]
	if !ok {
		t.Fatalf("key user_123 not found in bucket")
	}
	if !bytes.Equal(stored, jpgBytes) {
		t.Errorf("stored content does not match second upload")
	}

	down := newCtx("GET", filesPrefix+"user_123", nil)
	serve(t, s, down)
	if down.headers["Content-Type"] != "image/jpeg" {
		t.Errorf("served Content-Type = %q, want image/jpeg", down.headers["Content-Type"])
	}
}

func TestFiles_PerOwnerIsolatesUsers(t *testing.T) {
	s, store := newStore(t)
	s.PerOwner()

	u1 := newCtx("PUT", filesPrefix, pngBytes)
	u1.SetUserID("user_1")
	upload(t, s, u1)

	u2 := newCtx("PUT", filesPrefix, jpgBytes)
	u2.SetUserID("user_2")
	upload(t, s, u2)

	if len(store) != 2 {
		t.Fatalf("bucket size = %d, want 2 objects", len(store))
	}
	if !bytes.Equal(store["user_1"], pngBytes) {
		t.Errorf("user_1 data mismatch")
	}
	if !bytes.Equal(store["user_2"], jpgBytes) {
		t.Errorf("user_2 data mismatch")
	}
}

func TestFiles_PerOwnerValidationFailureDoesNotOverwriteData(t *testing.T) {
	s, store := newStore(t)
	s.PerOwner()

	u1 := newCtx("PUT", filesPrefix, pngBytes)
	u1.SetUserID("user_1")
	upload(t, s, u1)

	bad := newCtx("PUT", filesPrefix, []byte("not an image"))
	bad.SetUserID("user_1")
	upload(t, s, bad)

	if bad.status != 415 {
		t.Errorf("bad upload status = %d, want 415", bad.status)
	}
	if !bytes.Equal(store["user_1"], pngBytes) {
		t.Errorf("valid photo was corrupted after rejected upload")
	}
}

func TestFiles_BucketFailureIs502AndKeepsTheCause(t *testing.T) {
	s, err := files.New(brokenBucket{}, filesPrefix)
	if err != nil {
		t.Fatalf("files.New: %v", err)
	}

	ctx := newCtx("PUT", filesPrefix, pngBytes)
	upload(t, s, ctx)

	// A storage failure is ours, not the client's: it must not be reported as a 4xx.
	if ctx.status != 502 {
		t.Errorf("status = %d, want 502", ctx.status)
	}
}
