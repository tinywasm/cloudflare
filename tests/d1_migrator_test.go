//go:build !wasm

package cloudflare_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tinywasm/cloudflare/d1"
	"github.com/tinywasm/ddl"
	"github.com/tinywasm/fmt"
	"github.com/tinywasm/model"
	"github.com/tinywasm/sqlt"
)

type mockModel struct {
	id   string
	name string
}

func (m *mockModel) Schema() model.Fields {
	return model.Fields{
		{Name: "id", Type: model.Text(), DB: &model.FieldDB{PK: true}},
		{Name: "name", Type: model.Text()},
	}
}

func (m *mockModel) Pointers() []any {
	return []any{&m.id, &m.name}
}

func (m *mockModel) ModelName() string {
	return "mock_models"
}

func (m *mockModel) TableName() string {
	return "mock_models"
}

func (m *mockModel) EncodeFields(w model.FieldWriter) {}

func (m *mockModel) DecodeFields(r model.FieldReader) {}

func (m *mockModel) IsNil() bool {
	return m == nil
}

func TestD1Migrator_Validation(t *testing.T) {
	_, err := d1.NewMigrator("", "db", "tok")
	if err == nil {
		t.Fatal("expected error for empty accountID")
	}
	_, err = d1.NewMigrator("acc", "", "tok")
	if err == nil {
		t.Fatal("expected error for empty databaseID")
	}
	_, err = d1.NewMigrator("acc", "db", "")
	if err == nil {
		t.Fatal("expected error for empty apiToken")
	}
}

func TestD1Migrator_ExecSuccessAndAuthHeader(t *testing.T) {
	token := "my-secret-token"
	var receivedAuth string
	var receivedBody struct {
		SQL    string `json:"sql"`
		Params []any  `json:"params"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed reading request body: %v", err)
		}
		if err := json.Unmarshal(bodyBytes, &receivedBody); err != nil {
			t.Fatalf("failed unmarshaling request body: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success": true, "errors": [], "result": []}`))
	}))
	defer srv.Close()

	conn := d1.NewMigratorWithURL(srv.URL, token)
	err := conn.Exec("CREATE TABLE sample (id TEXT PRIMARY KEY);")
	if err != nil {
		t.Fatalf("unexpected Exec error: %v", err)
	}

	if receivedAuth != "Bearer my-secret-token" {
		t.Errorf("expected Authorization header 'Bearer my-secret-token', got %q", receivedAuth)
	}

	if receivedBody.SQL != "CREATE TABLE sample (id TEXT PRIMARY KEY);" {
		t.Errorf("expected sql 'CREATE TABLE sample (id TEXT PRIMARY KEY);', got %q", receivedBody.SQL)
	}
}

func TestD1Migrator_ErrorEnvelope(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success": false, "errors": [{"code": 7500, "message": "syntax error near 'SELCT'"}], "result": []}`))
	}))
	defer srv.Close()

	conn := d1.NewMigratorWithURL(srv.URL, "token")
	err := conn.Exec("SELCT 1;")
	if err == nil {
		t.Fatal("expected Exec error, got nil")
	}

	if !strings.Contains(err.Error(), "syntax error near 'SELCT'") {
		t.Errorf("expected error message to contain 'syntax error near 'SELCT'', got %q", err.Error())
	}
}

func TestD1Migrator_EndToEndDDLSync(t *testing.T) {
	var executedQueries []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("failed reading request body: %v", err)
		}
		var payload struct {
			SQL string `json:"sql"`
		}
		if err := json.Unmarshal(bodyBytes, &payload); err != nil {
			t.Fatalf("failed unmarshaling request body: %v", err)
		}
		executedQueries = append(executedQueries, payload.SQL)

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"success": true, "errors": [], "result": []}`))
	}))
	defer srv.Close()

	conn := d1.NewMigratorWithURL(srv.URL, "token")
	db := ddl.New(conn, sqlt.NewCompiler())

	m := &mockModel{}
	err := db.Sync(m)
	if err != nil {
		t.Fatalf("unexpected Sync error: %v", err)
	}

	if len(executedQueries) == 0 {
		t.Fatal("expected executed queries, got 0")
	}

	hasCreateTable := false
	for _, q := range executedQueries {
		if strings.Contains(q, "CREATE TABLE") {
			hasCreateTable = true
			break
		}
	}

	if !hasCreateTable {
		t.Errorf("expected a CREATE TABLE query, got queries: %v", fmt.Sprintf("%v", executedQueries))
	}
}
