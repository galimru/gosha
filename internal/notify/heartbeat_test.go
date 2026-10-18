package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPing(t *testing.T) {
	status := http.StatusOK
	pings := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pings++
		w.WriteHeader(status)
	}))
	defer srv.Close()
	if err := Ping(context.Background(), srv.URL); err != nil || pings != 1 {
		t.Fatalf("Ping = %v, %d pings", err, pings)
	}
	status = http.StatusNotFound
	if err := Ping(context.Background(), srv.URL); err == nil {
		t.Error("a 404 should be an error")
	}
}
