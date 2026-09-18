// Package kovarmodel uses the exact model paths and request schemas from
// kovar-new-api.json, including distinct OpenAI and Qwen image protocols.
package kovarmodel

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"sort"
	"strings"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"

	"kovar-gateway/internal/platform/httpx"
	"kovar-gateway/internal/platform/upstream"
)

//go:embed schemas/*.json
var schemas embed.FS

type KovarModelClient struct {
	http    *upstream.Client
	schemas map[string]*jsonschema.Schema
}

func New(base string, timeout time.Duration, log *slog.Logger) (*KovarModelClient, error) {
	c := &KovarModelClient{http: upstream.New(base, timeout, log), schemas: map[string]*jsonschema.Schema{}}
	for _, op := range []string{"chat", "image", "image_edit", "qwen_image", "qwen_image_edit", "video", "speech", "transcription", "embedding", "rerank"} {
		b, err := schemas.ReadFile("schemas/" + op + ".json")
		if err != nil {
			return nil, err
		}
		var doc any
		if err = json.Unmarshal(b, &doc); err != nil {
			return nil, err
		}
		compiler := jsonschema.NewCompiler()
		if err = compiler.AddResource("schema.json", doc); err != nil {
			return nil, err
		}
		schema, err := compiler.Compile("schema.json")
		if err != nil {
			return nil, err
		}
		c.schemas[op] = schema
	}
	return c, nil
}

type File struct {
	Name        string
	ContentType string
	Data        []byte
}
type Request struct {
	Operation string
	Payload   map[string]any
	Files     map[string]File
}
type Model struct {
	ID string `json:"id"`
}

func (c *KovarModelClient) Models(ctx context.Context, key string) ([]Model, error) {
	resp, err := c.http.Do(ctx, "GET", "/v1/models", "", nil, auth(key))
	if err != nil {
		return nil, err
	}
	raw, err := upstream.JSON(resp)
	if err != nil {
		return nil, err
	}
	var out struct {
		Data []Model `json:"data"`
	}
	if json.Unmarshal(raw, &out) != nil || out.Data == nil {
		return nil, httpx.E(502, "UPSTREAM_INVALID_RESPONSE", "model list lacks data array")
	}
	return out.Data, nil
}
func (c *KovarModelClient) Validate(r Request) error {
	if r.Payload == nil {
		return httpx.Invalid("model payload must be an object")
	}
	if err := validateUploads(r); err != nil {
		return err
	}
	schema, ok := c.schemas[r.Operation]
	if !ok {
		return httpx.NotSupported("task protocol is not supported")
	}
	b, err := json.Marshal(r.Payload)
	if err != nil {
		return httpx.Invalid("invalid model payload")
	}
	var v any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if d.Decode(&v) != nil {
		return httpx.Invalid("invalid model payload")
	}
	if r.Operation == "image_edit" || r.Operation == "transcription" {
		m := v.(map[string]any)
		for field, file := range r.Files {
			if len(file.Data) == 0 || len(file.Data) > 8<<20 {
				return httpx.Invalid("file must be 1 byte to 8 MiB")
			}
			m[field] = file.Name
		}
	}
	if err = schema.Validate(v); err != nil {
		return httpx.Invalid("payload does not match documented Kovar request schema")
	}
	if r.Operation != "chat" {
		if _, ok := r.Payload["stream"]; ok {
			return httpx.NotSupported("streaming is supported only for chat")
		}
	}
	return nil
}
func (c *KovarModelClient) Call(ctx context.Context, key string, r Request) (*http.Response, error) {
	if err := c.Validate(r); err != nil {
		return nil, err
	}
	paths := map[string]string{"chat": "/v1/chat/completions", "image": "/v1/images/generations/", "image_edit": "/v1/images/edits/", "qwen_image": "/v1/images/generations", "qwen_image_edit": "/v1/images/edits", "video": "/v1/video/generations", "speech": "/v1/audio/speech", "transcription": "/v1/audio/transcriptions", "embedding": "/v1/embeddings", "rerank": "/v1/rerank"}
	contentType := "application/json"
	b, err := json.Marshal(r.Payload)
	if err != nil {
		return nil, err
	}
	if r.Operation == "image_edit" || r.Operation == "transcription" {
		b, contentType, err = encodeMultipart(r)
		if err != nil {
			return nil, err
		}
	}
	return c.http.Do(ctx, "POST", paths[r.Operation], contentType, b, auth(key))
}
func (c *KovarModelClient) Video(ctx context.Context, key, id string) (json.RawMessage, error) {
	if !httpx.Identifier(id) {
		return nil, httpx.E(502, "UPSTREAM_INVALID_RESPONSE", "invalid upstream video task id")
	}
	resp, err := c.http.Do(ctx, "GET", "/v1/video/generations/"+id, "", nil, auth(key))
	if err != nil {
		return nil, err
	}
	return upstream.JSON(resp)
}
func auth(key string) http.Header { return http.Header{"Authorization": []string{"Bearer " + key}} }
func encodeMultipart(r Request) ([]byte, string, error) {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	names := make([]string, 0, len(r.Payload))
	for k := range r.Payload {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		v := r.Payload[k]
		if arr, ok := v.([]any); ok {
			for _, x := range arr {
				if err := w.WriteField(k, fmt.Sprint(x)); err != nil {
					return nil, "", err
				}
			}
		} else {
			if err := w.WriteField(k, fmt.Sprint(v)); err != nil {
				return nil, "", err
			}
		}
	}
	for k, file := range r.Files {
		if strings.ContainsAny(k+file.Name, "\r\n\"") {
			return nil, "", httpx.Invalid("invalid upload name")
		}
		h := textproto.MIMEHeader{}
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, k, file.Name))
		h.Set("Content-Type", file.ContentType)
		part, err := w.CreatePart(h)
		if err != nil {
			return nil, "", err
		}
		if _, err = io.Copy(part, bytes.NewReader(file.Data)); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return b.Bytes(), w.FormDataContentType(), nil
}
