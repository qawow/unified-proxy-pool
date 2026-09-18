package web

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"modernc.org/sqlite"
)

// Driver-level failures are internal errors: 500 with a generic message, not
// the raw sqlite error leaking to the client as a 400.
func TestWriteErrorMapsDriverErrorTo500(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, fmt.Errorf("fetch subscription: %w", &sqlite.Error{}))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var body struct {
		Message string `json:"message"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Message != "internal error" {
		t.Fatalf("message = %q, want generic internal error", body.Message)
	}
}

// Wrapped driver errors must still be recognised — services almost always
// annotate them.
func TestWriteErrorWrappedDriverError(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", &sqlite.Error{})))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// Plain validation/client errors keep the existing 400 behaviour.
func TestWriteErrorKeepsClientErrors(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, errors.New("name is required"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var body map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if !strings.Contains(fmt.Sprint(body["message"]), "name is required") {
		t.Fatalf("client error message not echoed: %v", body)
	}
}

func TestWriteErrorNoRowsIs404(t *testing.T) {
	rec := httptest.NewRecorder()
	writeError(rec, sql.ErrNoRows)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}
