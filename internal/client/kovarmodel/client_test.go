package kovarmodel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func object(s string) map[string]any {
	var m map[string]any
	d := json.NewDecoder(strings.NewReader(s))
	d.UseNumber()
	_ = d.Decode(&m)
	return m
}
func TestModelDocumentedOperations(t *testing.T) {
	cases := []struct {
		op, path, payload string
		files             map[string]File
	}{{"chat", "/v1/chat/completions", `{"model":"fixture","messages":[{"role":"user","content":"Hi"}],"tools":[],"tool_choice":"auto","response_format":{"type":"json_object"}}`, nil}, {"image", "/v1/images/generations/", `{"model":"fixture","prompt":"image"}`, nil}, {"qwen_image", "/v1/images/generations", `{"model":"fixture","input":{"messages":[{"role":"user","content":[{"text":"image"}]}]}}`, nil}, {"qwen_image_edit", "/v1/images/edits", `{"model":"fixture","input":{"messages":[{"role":"user","content":[{"image":"https://example.com/image.png"},{"text":"edit"}]}]}}`, nil}, {"image_edit", "/v1/images/edits/", `{"model":"fixture","prompt":"edit"}`, map[string]File{"image": {Name: "fixture.png", ContentType: "image/png", Data: fixturePNG()}}}, {"video", "/v1/video/generations", `{"model":"fixture","prompt":"video","duration":5}`, nil}, {"speech", "/v1/audio/speech", `{"model":"fixture","input":"hello","voice":"alloy"}`, nil}, {"transcription", "/v1/audio/transcriptions", `{"model":"fixture"}`, map[string]File{"file": {Name: "fixture.wav", ContentType: "audio/wav", Data: []byte("fixture-audio")}}}, {"embedding", "/v1/embeddings", `{"model":"fixture","input":"hello"}`, nil}, {"rerank", "/v1/rerank", `{"model":"fixture","query":"hello","documents":["a","b"]}`, nil}}
	for _, tc := range cases {
		t.Run(tc.op, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != tc.path || r.Header.Get("Authorization") != "Bearer sk-fixture" {
					t.Error("incorrect model path/method/auth")
				}
				if len(tc.files) > 0 {
					if err := r.ParseMultipartForm(1 << 20); err != nil {
						t.Error(err)
					}
					for field := range tc.files {
						f, _, err := r.FormFile(field)
						if err != nil {
							t.Error(err)
						} else {
							f.Close()
						}
					}
				}
				fmt.Fprint(w, `{"data":[]}`)
			}))
			defer s.Close()
			c, err := New(s.URL, time.Second, nil)
			if err != nil {
				t.Fatal(err)
			}
			r, err := c.Call(context.Background(), "sk-fixture", Request{Operation: tc.op, Payload: object(tc.payload), Files: tc.files})
			if err != nil {
				t.Fatal(err)
			}
			r.Body.Close()
		})
	}
}
func TestModelListVideoAndSSE(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			fmt.Fprint(w, `{"data":[{"id":"fixture"}]}`)
		case "/v1/video/generations/task1":
			fmt.Fprint(w, `{"task_id":"task1","status":"completed"}`)
		case "/v1/chat/completions":
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[]}\n\n")
			w.(http.Flusher).Flush()
			fmt.Fprint(w, "data: [DONE]\n\n")
		default:
			w.WriteHeader(404)
		}
	}))
	defer s.Close()
	c, err := New(s.URL, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	if m, err := c.Models(context.Background(), "sk-fixture"); err != nil || len(m) != 1 {
		t.Fatal(m, err)
	}
	if _, err = c.Video(context.Background(), "sk-fixture", "task1"); err != nil {
		t.Fatal(err)
	}
	r, err := c.Call(context.Background(), "sk-fixture", Request{Operation: "chat", Payload: object(`{"model":"fixture","stream":true,"messages":[]}`)})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, err := io.ReadAll(r.Body)
	if err != nil || !strings.Contains(string(b), "[DONE]") {
		t.Fatal("SSE failed", err)
	}
}
func TestModelErrors(t *testing.T) {
	for _, status := range []int{401, 429, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(status) }))
			defer s.Close()
			c, err := New(s.URL, time.Second, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = c.Call(context.Background(), "sk-fixture", Request{Operation: "chat", Payload: object(`{"model":"fixture","messages":[]}`)}); err == nil {
				t.Fatal("upstream error ignored")
			}
			if calls.Load() != 1 {
				t.Fatal("billable POST retried")
			}
		})
	}
	t.Run("timeout", func(t *testing.T) {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
			}
		}))
		defer s.Close()
		c, _ := New(s.URL, 20*time.Millisecond, nil)
		if _, err := c.Call(context.Background(), "sk-fixture", Request{Operation: "chat", Payload: object(`{"model":"fixture","messages":[]}`)}); err == nil {
			t.Fatal("timeout ignored")
		}
	})
	t.Run("malformed JSON", func(t *testing.T) {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `invalid`) }))
		defer s.Close()
		c, _ := New(s.URL, time.Second, nil)
		if _, err := c.Models(context.Background(), "sk-fixture"); err == nil {
			t.Fatal("malformed JSON accepted")
		}
	})
}

func fixturePNG() []byte {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewNRGBA(image.Rect(0, 0, 1, 1)))
	return b.Bytes()
}

func TestRejectInvalidUploadAndUnknownFields(t *testing.T) {
	c, err := New("https://example.invalid", time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	cases := []Request{{Operation: "transcription", Payload: nil}, {Operation: "transcription", Payload: object(`{"model":"fixture","file":"not an upload"}`)}, {Operation: "image_edit", Payload: object(`{"model":"fixture","prompt":"edit"}`), Files: map[string]File{"image": {Name: "image.png", Data: []byte("invalid")}}}, {Operation: "chat", Payload: object(`{"model":"fixture","messages":[],"private_key":"forbidden"}`)}}
	for _, r := range cases {
		if err = c.Validate(r); err == nil {
			t.Fatal("invalid upload or unknown credential field accepted")
		}
	}
}
