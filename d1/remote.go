//go:build !wasm

package d1

import (
	"bytes"
	"encoding/json"
	"net/http"

	"github.com/tinywasm/ddl"
	. "github.com/tinywasm/fmt"
)

// cloudflareAPIBase is Cloudflare's REST API version root. It is a protocol
// detail fixed for every caller — not a per-environment setting — so it is
// a constant here rather than a NewMigrator parameter. If Cloudflare bumps
// the version, this is the one line that changes; every existing caller
// (including tests, via NewMigratorWithURL) keeps compiling.
const cloudflareAPIBase = "https://api.cloudflare.com/client/v4"

// NewMigrator opens a D1 database over Cloudflare's HTTP Query API, for
// running schema migrations from CI — outside a Worker, where the NewEdge
// binding does not exist.
//
// It is named for what it does rather than for its transport, and its return
// type says the same thing: ddl.Execer is exactly "somewhere to send DDL",
// so this connection cannot be mistaken for one you can read rows from.
// Inside a Worker, use NewEdge.
//
// Because it is not a storage.Conn, ddl.Sync takes its additive path —
// CREATE TABLE plus ADD COLUMN, no safe-drop probe. That is the same path
// NewEdge already produces in production today (the WASM adapter does not
// implement ddl.TableIntrospector either), so migrating through this
// transport does not change migration behavior.
//
// Usage:
//
//	conn, err := d1.NewMigrator(accountID, databaseID, apiToken)
//	err = ddl.New(conn, sqlt.NewCompiler()).Sync(models...)
func NewMigrator(accountID, databaseID, apiToken string) (ddl.Execer, error) {
	if accountID == "" || databaseID == "" || apiToken == "" {
		return nil, Err(errPrefix + "NewMigrator requires accountID, databaseID and apiToken")
	}
	return NewMigratorWithURL(
		cloudflareAPIBase+"/accounts/"+accountID+"/d1/database/"+databaseID+"/query",
		apiToken,
	), nil
}

// NewMigratorWithURL is the test seam — it lets tests point the client at an
// httptest.Server instead of the real Cloudflare API.
func NewMigratorWithURL(url, apiToken string) *migratorConn {
	return &migratorConn{url: url, token: apiToken, client: http.DefaultClient}
}

// migratorConn carries no Compiler: ddl.New takes the DDL compiler as its
// second argument (sqlt.NewCompiler()), and without storage.Compiler this
// type cannot be mistaken for a queryable connection.
type migratorConn struct {
	url    string
	token  string
	client *http.Client
}

// Exec POSTs one statement to D1's Query API and reports whether it applied.
// Request:  {"sql": "...", "params": [...]}
// Response: {"success": bool, "errors": [{code,message}], "result": [...]}
func (m *migratorConn) Exec(query string, args ...any) error {
	params := make([]any, len(args))
	copy(params, args)
	body, err := json.Marshal(struct {
		SQL    string `json:"sql"`
		Params []any  `json:"params"`
	}{SQL: query, Params: params})
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, m.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+m.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	var envelope struct {
		Success bool `json:"success"`
		Errors  []struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return Err(errPrefix + "could not decode the D1 response")
	}
	if !envelope.Success || resp.StatusCode >= 400 {
		if len(envelope.Errors) > 0 {
			// Name the real cause: a migration that fails with a generic
			// "request failed" is undiagnosable from a CI log.
			return Err(errPrefix + envelope.Errors[0].Message)
		}
		return Err(errPrefix + "the D1 query failed")
	}
	return nil
}

var _ ddl.Execer = (*migratorConn)(nil)
