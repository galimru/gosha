package notion

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClient(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("Notion-Version") != Version {
			t.Errorf("headers: %v", r.Header)
		}
		switch {
		case r.URL.Path == "/blocks/b1/children" && r.URL.Query().Get("start_cursor") == "":
			w.Write([]byte(`{"results":[{"id":"x","type":"paragraph","paragraph":{"rich_text":[]}}],"has_more":true,"next_cursor":"c2"}`))
		case r.URL.Path == "/blocks/b1/children":
			w.Write([]byte(`{"results":[{"id":"y","type":"divider","divider":{}}],"has_more":false}`))
		case r.URL.Path == "/pages/busy" && calls < 4:
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"code":"rate_limited","message":"slow down"}`))
		case r.URL.Path == "/pages/busy":
			w.Write([]byte(`{"id":"busy","properties":{"Name":{"type":"title","title":[{"plain_text":"Busy"}]}}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"code":"object_not_found","message":"Could not find page"}`))
		}
	}))
	defer srv.Close()
	c := New("tok", WithURL(srv.URL), WithBackoff(time.Millisecond))
	ctx := context.Background()

	bs, err := c.Children(ctx, "b1")
	if err != nil || len(bs) != 2 || bs[1].Type != "divider" || string(bs[1].Data) != "{}" {
		t.Fatalf("children = %+v, %v", bs, err)
	}
	p, err := c.Page(ctx, "busy")
	if err != nil || p.Title() != "Busy" {
		t.Fatalf("retried page = %+v, %v", p, err)
	}
	if _, err := c.Page(ctx, "missing"); !errors.Is(err, ErrNoAccess) {
		t.Errorf("missing page err = %v", err)
	}
}
