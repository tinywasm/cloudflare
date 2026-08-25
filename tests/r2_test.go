//go:build wasm

package cloudflare_test

import (
	"bytes"
	"testing"

	"github.com/tinywasm/cloudflare/r2"
)

func TestR2_BinaryRoundtrip(t *testing.T) {
	_ = setupEnv(t)

	bucket, err := r2.NewEdge("FILES")
	if err != nil {
		t.Fatalf("failed to connect to bucket: %v", err)
	}

	// Non-UTF8 bytes that would be corrupted if treated as string
	original := []byte{0xFF, 0xFE, 0x00, 0x80, 0x21, 0x42}
	key := "test.bin"
	contentType := "application/octet-stream"

	if err := bucket.Put(key, original, contentType); err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	retrieved, ct, err := bucket.Get(key)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if ct != contentType {
		t.Errorf("expected content type %s, got %s", contentType, ct)
	}

	if !bytes.Equal(original, retrieved) {
		t.Errorf("binary roundtrip failed:\nwant: %v\ngot:  %v", original, retrieved)
	}
}

func TestR2_NotFound(t *testing.T) {
	_ = setupEnv(t)
	bucket, _ := r2.NewEdge("FILES")

	_, _, err := bucket.Get("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent key, got nil")
	}
}
