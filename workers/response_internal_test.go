//go:build wasm

package workers

import (
	"testing"
)

// TestResponse_WriteAccumulatesBody comprueba que buf ([]byte con append)
// concatena exactamente igual que lo hacía bytes.Buffer: Write y WriteString
// alternados, incluyendo UTF-8 multibyte y un Write de slice vacío.
func TestResponse_WriteAccumulatesBody(t *testing.T) {
	w := newResponse()

	n, err := w.Write([]byte("hola "))
	if err != nil || n != 5 {
		t.Fatalf("Write #1: n=%d err=%v", n, err)
	}

	n, err = w.WriteString("mundo ")
	if err != nil || n != 6 {
		t.Fatalf("WriteString #1: n=%d err=%v", n, err)
	}

	// UTF-8 multibyte: "café" tiene 5 bytes (é ocupa 2).
	n, err = w.WriteString("café ")
	if err != nil || n != 6 {
		t.Fatalf("WriteString multibyte: n=%d err=%v", n, err)
	}

	n, err = w.Write(nil)
	if err != nil || n != 0 {
		t.Fatalf("Write vacío: n=%d err=%v", n, err)
	}

	n, err = w.Write([]byte("fin"))
	if err != nil || n != 3 {
		t.Fatalf("Write #2: n=%d err=%v", n, err)
	}

	want := "hola mundo café fin"
	if got := string(w.buf); got != want {
		t.Fatalf("cuerpo acumulado = %q, se esperaba %q", got, want)
	}
}

func TestResponse_WriteStringEmpty(t *testing.T) {
	w := newResponse()
	if n, err := w.WriteString(""); err != nil || n != 0 {
		t.Fatalf("WriteString(\"\"): n=%d err=%v", n, err)
	}
	if len(w.buf) != 0 {
		t.Fatalf("buf debería seguir vacío, tiene %d bytes", len(w.buf))
	}
}
