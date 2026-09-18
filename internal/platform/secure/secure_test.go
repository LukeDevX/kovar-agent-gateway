package secure

import (
	"encoding/base64"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/crypto"
)

func TestEIP191(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	a, _ := Address(crypto.PubkeyToAddress(key.PublicKey).Hex())
	message := Registration(a, Random(), time.Now().Unix())
	sig, err := crypto.Sign(PersonalHash(message), key)
	if err != nil {
		t.Fatal(err)
	}
	encoded := "0x" + hex.EncodeToString(sig)
	if !Verify(a, message, encoded) {
		t.Fatal("valid signature rejected")
	}
	sig[64] += 27
	if !Verify(a, message, "0x"+hex.EncodeToString(sig)) {
		t.Fatal("27/28 recovery id rejected")
	}
	other, _ := crypto.GenerateKey()
	if Verify(crypto.PubkeyToAddress(other.PublicKey).Hex(), message, encoded) || Verify(a, message+"tamper", encoded) || Verify(a, message, "0x00") {
		t.Fatal("invalid signature accepted")
	}
}
func TestCanonicalBindsQueryBodyAndIdempotency(t *testing.T) {
	base := Canonical("POST", "/api/v1/tasks?page=1", "100", "nonce", "one", []byte(`{}`))
	for _, changed := range []string{Canonical("POST", "/api/v1/tasks?page=2", "100", "nonce", "one", []byte(`{}`)), Canonical("POST", "/api/v1/tasks?page=1", "100", "nonce", "two", []byte(`{}`)), Canonical("POST", "/api/v1/tasks?page=1", "100", "nonce", "one", []byte(`{"x":1}`))} {
		if base == changed {
			t.Fatal("canonical signature omitted authenticated data")
		}
	}
}
func TestEncryption(t *testing.T) {
	e, err := NewEncryption(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("e", 32))))
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte("fixture-api-key")
	cipher, err := e.Encrypt(plain, "agent-a:model-key")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(cipher), string(plain)) {
		t.Fatal("plaintext stored")
	}
	got, err := e.Decrypt(cipher, "agent-a:model-key")
	if err != nil || string(got) != string(plain) {
		t.Fatal("decrypt failed")
	}
	if _, err = e.Decrypt(cipher, "agent-b:model-key"); err == nil {
		t.Fatal("cross-agent ciphertext accepted")
	}
	cipher[len(cipher)-1] ^= 1
	if _, err = e.Decrypt(cipher, "agent-a:model-key"); err == nil {
		t.Fatal("tampered ciphertext accepted")
	}
	if _, err = e.Decrypt(nil, "x"); err == nil {
		t.Fatal("short ciphertext accepted")
	}
}
func TestTimestamp(t *testing.T) {
	now := time.Now()
	for _, ts := range []int64{now.Add(-time.Hour).Unix(), now.Add(time.Hour).Unix(), -1 << 63, 1<<63 - 1} {
		if Timestamp(ts, now, 5*time.Minute) {
			t.Fatal("invalid timestamp accepted")
		}
	}
	if !Timestamp(now.Unix(), now, 5*time.Minute) {
		t.Fatal("valid timestamp rejected")
	}
}
