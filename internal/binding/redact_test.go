package binding

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSanitizeNestedCredentials(t *testing.T) {
	raw := json.RawMessage(`{"password":"hidden","nested":{"Authorization":"hidden","key":"hidden","session":"hidden","content":"fixture-session"},"items":[{"api_key":"hidden"}],"quota":1234567890123456789}`)
	out := string(sanitize(raw, "fixture-session"))
	if strings.Contains(out, "hidden") || strings.Contains(out, "fixture-session") || !strings.Contains(out, "1234567890123456789") {
		t.Fatal("redaction leaked credentials or lost integer precision")
	}
}

func TestSanitizeScalarAndExactDecimal(t *testing.T) {
	if got := string(sanitize(json.RawMessage(`"fixture-session"`), "fixture-session")); got != `"[REDACTED]"` {
		t.Fatal("scalar management data leaked a credential")
	}
	if got := string(sanitize(json.RawMessage(`{"money":9007199254740993.01}`))); got != `{"money":9007199254740993.01}` {
		t.Fatal("topup money lost decimal precision")
	}
}
