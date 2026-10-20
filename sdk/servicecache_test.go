package sdk

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestServiceForCache(t *testing.T) {
	var calls atomic.Int32
	var refuse, down atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		switch {
		case down.Load():
			http.Error(w, "core restarting", http.StatusBadGateway)
		case refuse.Load():
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"immich: switched off","reason":"off_person"}`))
		default:
			_, _ = w.Write([]byte(`{"type":"immich","url":"http://immich","key":"k1","source":"personal"}`))
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	c := New(srv.URL, Options{Token: "pca_x", ServiceCache: 50 * time.Millisecond, ServiceStale: time.Hour})

	if got, err := c.ServiceFor(ctx, "a@b.c", "immich"); err != nil || got.Key != "k1" {
		t.Fatalf("first: %+v %v", got, err)
	}
	if _, _ = c.ServiceFor(ctx, "a@b.c", "immich"); calls.Load() != 1 {
		t.Fatalf("reused within the cache window: %d calls", calls.Load())
	}
	// Core goes away: the last answer carries on.
	time.Sleep(60 * time.Millisecond)
	down.Store(true)
	if got, err := c.ServiceFor(ctx, "a@b.c", "immich"); err != nil || got.Key != "k1" {
		t.Fatalf("stale while Core is down: %+v %v", got, err)
	}
	// Core says no: dropped at once, no stale answer after that.
	down.Store(false)
	refuse.Store(true)
	time.Sleep(60 * time.Millisecond)
	var le *LinkError
	if _, err := c.ServiceFor(ctx, "a@b.c", "immich"); !errors.As(err, &le) || le.Reason != "off_person" {
		t.Fatalf("refusal: %v", err)
	}
	down.Store(true)
	if _, err := c.ServiceFor(ctx, "a@b.c", "immich"); err == nil {
		t.Fatal("after a refusal nothing stale may be served")
	}
	// Fresh never uses the cache.
	down.Store(false)
	refuse.Store(false)
	n := calls.Load()
	c.ServiceForFresh(ctx, "a@b.c", "immich")
	c.ServiceForFresh(ctx, "a@b.c", "immich")
	if calls.Load() != n+2 {
		t.Fatal("ServiceForFresh must ask every time")
	}
}
