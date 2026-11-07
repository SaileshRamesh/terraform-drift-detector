package state

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPReader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != "ci" || p != "secret" {
			http.Error(w, "nope", http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"version": 4}`))
	}))
	defer srv.Close()

	url := strings.Replace(srv.URL, "http://", "http://ci:secret@", 1)
	r := HTTPReader{URL: url}
	data, err := r.Read(context.Background())
	if err != nil || string(data) != `{"version": 4}` {
		t.Fatalf("got %q, %v", data, err)
	}
	if strings.Contains(r.Source(), "secret") {
		t.Errorf("Source leaks the password: %s", r.Source())
	}

	if _, err := (HTTPReader{URL: srv.URL}).Read(context.Background()); err == nil {
		t.Error("expected error on 401")
	}
}
