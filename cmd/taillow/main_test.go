package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRoutes(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantAllow  string
		wantBody   string
	}{
		{"healthz answers GET", http.MethodGet, "/healthz", http.StatusOK, "", `{"status":"ok"}` + "\n"},
		{"healthz refuses POST and says what is allowed", http.MethodPost, "/healthz", http.StatusMethodNotAllowed, "GET, HEAD", ""},
		{"unknown path is 404", http.MethodGet, "/nope", http.StatusNotFound, "", ""},
	}

	mux := newMux()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if got := rec.Header().Get("Allow"); got != tc.wantAllow {
				t.Errorf("Allow = %q, want %q", got, tc.wantAllow)
			}
			if tc.wantBody != "" && rec.Body.String() != tc.wantBody {
				t.Errorf("body = %q, want %q", rec.Body.String(), tc.wantBody)
			}
		})
	}
}
