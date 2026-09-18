package binding

import (
	"bytes"
	"encoding/json"
	"strings"
)

func sanitize(raw json.RawMessage, secrets ...string) json.RawMessage {
	var v any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&v) != nil {
		return json.RawMessage(`null`)
	}
	redact(v, secrets)
	if text, ok := v.(string); ok {
		for _, secret := range secrets {
			if secret != "" {
				text = strings.ReplaceAll(text, secret, "[REDACTED]")
			}
		}
		v = text
	}
	b, _ := json.Marshal(v)
	return b
}
func redact(v any, secrets []string) {
	switch x := v.(type) {
	case map[string]any:
		for k, value := range x {
			lower := strings.ToLower(strings.ReplaceAll(k, "-", "_"))
			if strings.Contains(lower, "password") || strings.Contains(lower, "secret") || strings.Contains(lower, "private_key") || lower == "key" || lower == "api_key" || lower == "access_token" || lower == "refresh_token" || lower == "authorization" || lower == "cookie" || lower == "session" {
				delete(x, k)
				continue
			}
			if s, ok := value.(string); ok {
				for _, secret := range secrets {
					if secret != "" {
						s = strings.ReplaceAll(s, secret, "[REDACTED]")
					}
				}
				x[k] = s
			} else {
				redact(value, secrets)
			}
		}
	case []any:
		for i, value := range x {
			if s, ok := value.(string); ok {
				for _, secret := range secrets {
					if secret != "" {
						s = strings.ReplaceAll(s, secret, "[REDACTED]")
					}
				}
				x[i] = s
			} else {
				redact(value, secrets)
			}
		}
	}
}
