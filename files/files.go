//go:build wasm

// Package files serves an object bucket over a route prefix.
//
// It exists so that every module that accepts user uploads gets the same three
// guarantees without rewriting them: the stored type is the one deduced from the
// bytes, the key is minted by the server, and the size is checked before a single
// byte of the body is buffered.
package files

import (
	"github.com/tinywasm/cloudflare/log"
	"github.com/tinywasm/filetype"
	"github.com/tinywasm/fmt"
	"github.com/tinywasm/model"
	"github.com/tinywasm/router"
	"github.com/tinywasm/unixid"
)

const (
	// DefaultMaxSize is the largest upload accepted unless the caller says otherwise.
	DefaultMaxSize = 10 << 20 // 10 MiB

	headerContentLength = "Content-Length"
	headerContentType   = "Content-Type"
	headerNoSniff       = "X-Content-Type-Options"
	valueNoSniff        = "nosniff"
)

// Bucket is the storage this package needs. r2.Bucket satisfies it.
type Bucket interface {
	Put(key string, data []byte, contentType string) error
	Get(key string) (data []byte, contentType string, err error)
}

// Store registers the upload and serve handlers for one bucket under one prefix.
type Store struct {
	bucket   Bucket
	ids      *unixid.UnixID
	allow    filetype.Allowlist
	prefix   string
	maxSize  int
	perOwner bool
}

// New builds a Store on the safe defaults: raster images only, 10 MiB.
// prefix must end in "/" — it is matched by prefix, and the key is what hangs off it.
func New(b Bucket, prefix string) (*Store, error) {
	if b == nil {
		return nil, fmt.Err("files: nil bucket")
	}
	if prefix == "" || prefix[len(prefix)-1] != '/' {
		return nil, fmt.Err("files: prefix must end in /, got", prefix)
	}
	ids, err := unixid.NewUnixID()
	if err != nil {
		return nil, fmt.Err("files: id generator:", err.Error())
	}
	return &Store{
		bucket:  b,
		ids:     ids,
		allow:   filetype.Images,
		prefix:  prefix,
		maxSize: DefaultMaxSize,
	}, nil
}

// Allow narrows or widens the accepted types. Never add SVG or HTML: both carry
// JavaScript and, served from your own domain, execute in your origin.
func (s *Store) Allow(a filetype.Allowlist) *Store {
	s.allow = a
	return s
}

// MaxSize caps the upload size in bytes.
func (s *Store) MaxSize(n int) *Store {
	s.maxSize = n
	return s
}

// PerOwner keys the object by its uploader: ONE object per identity, replaced whenever that
// identity uploads again. The natural shape of an avatar, a profile photo, a signature.
//
// The key is the caller's UserID and nothing else — no extension. The type is NOT lost: it
// was deduced from the bytes and travels in the object's metadata, which is exactly where
// serve() already reads it from. An extension would defeat the point: uploading a .png and
// then a .jpg would produce TWO keys, and the first would be orphaned forever.
//
// It is only reachable on a guarded route (upload already Requires files/Create), so the
// identity is never empty by construction.
func (s *Store) PerOwner() *Store {
	s.perOwner = true
	return s
}

// Resource is the permission an app must grant to let a caller upload. It is exported
// because the app is the one that decides WHO holds it: the library states the requirement,
// the consumer's Authorizer answers it.
const Resource model.Resource = "files"

// Action is what uploading does: it CREATES an object under a key the server generates.
// It is not "write" — the action vocabulary is closed CRUD, and an invented verb is a
// permission nothing enforces.
const Action = model.Create

// Mount registers both routes: uploading requires the files/Create permission, serving is
// public because an <img src> cannot send headers.
//
// The upload is guarded on purpose. Do NOT make it public to "fix" a 403: a write-open
// bucket is a spam form. If uploads are rejected, the app has not told the router who the
// caller is (edge.Config.Authn) or who may write (edge.Config.Authorize).
func (s *Store) Mount(r router.Router) {
	r.Put(s.prefix, s.upload).Requires(Resource, Action)
	r.Get(s.prefix, s.serve).Public()
}

func (s *Store) upload(ctx router.Context) {
	// Size first: Body() is lazy, so nothing has been buffered yet.
	if n, ok := contentLength(ctx); ok && n > s.maxSize {
		log.Reject(413, ctx.Method(), ctx.Path(), "declared size exceeds the limit")
		ctx.WriteStatus(413)
		return
	}

	data := ctx.Body()
	if len(data) > s.maxSize {
		log.Reject(413, ctx.Method(), ctx.Path(), "body exceeds the limit")
		ctx.WriteStatus(413)
		return
	}

	// The type comes from the bytes. The client's Content-Type is text it chose.
	t, err := s.allow.Validate(data)
	if err != nil {
		// The reason names what the bytes actually were ("SVG is not allowed"), which is
		// the difference between diagnosing an attack and staring at a 415.
		log.Reject(415, ctx.Method(), ctx.Path(), err.Error())
		ctx.WriteStatus(415)
		ctx.Write([]byte(err.Error()))
		return
	}

	// The key comes from the server. The client's filename is text it chose.
	key := s.ids.NewID() + t.Ext
	if s.perOwner {
		key = ctx.UserID()
	}

	if err := s.bucket.Put(key, data, t.MIME); err != nil {
		log.Fail(502, ctx.Method(), ctx.Path(), err)
		ctx.WriteStatus(502)
		return
	}

	ctx.WriteStatus(201)
	ctx.Write([]byte(key)) // the client learns here where its file landed
}

func (s *Store) serve(ctx router.Context) {
	key := ctx.Path()[len(s.prefix):]
	if key == "" {
		log.Reject(400, ctx.Method(), ctx.Path(), "no key in path")
		ctx.WriteStatus(400)
		return
	}

	data, ct, err := s.bucket.Get(key)
	if err != nil {
		// A missing key and a broken bucket both land here, so the cause is the only way
		// to tell "that file was never uploaded" from "R2 is down".
		log.Reject(404, ctx.Method(), ctx.Path(), err.Error())
		ctx.WriteStatus(404)
		return
	}

	ctx.SetHeader(headerContentType, ct)       // the type we deduced on upload
	ctx.SetHeader(headerNoSniff, valueNoSniff) // the browser does not get to guess
	ctx.Write(data)
}

// contentLength reports the declared body size. ok is false when the header is
// absent or unparseable — the caller then falls back to checking the read body.
func contentLength(ctx router.Context) (n int, ok bool) {
	raw := ctx.GetHeader(headerContentLength)
	if raw == "" {
		return 0, false
	}
	n, err := fmt.Convert(raw).Int()
	if err != nil {
		return 0, false
	}
	return n, true
}
