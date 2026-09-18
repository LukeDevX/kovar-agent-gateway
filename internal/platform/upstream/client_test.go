package upstream

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestGETBackoffAndNoRedirect(t *testing.T) {
	var calls atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(503)
			return
		}
		fmt.Fprint(w, `{}`)
	}))
	defer s.Close()
	c := New(s.URL, time.Second, nil)
	resp, err := c.Do(context.Background(), "GET", "/v1/models", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if calls.Load() != 3 {
		t.Fatal("GET retries not bounded correctly")
	}
	var leaked atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer redirect.Close()
	c = New(redirect.URL, time.Second, nil)
	if _, err = c.Do(context.Background(), "POST", "/api/user/login", "application/json", []byte(`{}`), http.Header{"Authorization": []string{"Bearer fixture"}}); err == nil {
		t.Fatal("redirect should be rejected")
	}
	if leaked.Load() {
		t.Fatal("credentials followed upstream redirect")
	}
}
