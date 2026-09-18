package secure

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func Random() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("secure random unavailable")
	}
	return hex.EncodeToString(b)
}
func Hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func Address(s string) (string, error) {
	if len(s) != 42 || !strings.HasPrefix(s, "0x") || !common.IsHexAddress(s) {
		return "", errors.New("invalid EVM address")
	}
	return strings.ToLower(s), nil
}
func Registration(address, nonce string, timestamp int64) string {
	return fmt.Sprintf("Kovar Agent Gateway\n\nAction: Register\nAddress: %s\nNonce: %s\nTimestamp: %d", address, nonce, timestamp)
}

// target is the escaped path plus the exact raw query. Binding the query and
// idempotency key prevents a signed request from being changed into another write.
func Canonical(method, target, timestamp, nonce, idempotency string, body []byte) string {
	return "Kovar Agent Gateway\nAction: Request\n" + method + "\n" + target + "\n" + timestamp + "\n" + nonce + "\n" + Hash(body) + "\n" + idempotency
}
func PersonalHash(message string) []byte {
	return crypto.Keccak256([]byte("\x19Ethereum Signed Message:\n" + strconv.Itoa(len([]byte(message))) + message))
}
func Verify(address, message, signature string) bool {
	if len(signature) != 132 || !strings.HasPrefix(signature, "0x") {
		return false
	}
	sig, err := hex.DecodeString(signature[2:])
	if err != nil {
		return false
	}
	if sig[64] >= 27 {
		sig[64] -= 27
	}
	if !crypto.ValidateSignatureValues(sig[64], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:64]), true) {
		return false
	}
	pub, err := crypto.SigToPub(PersonalHash(message), sig)
	return err == nil && strings.EqualFold(crypto.PubkeyToAddress(*pub).Hex(), address)
}
func Timestamp(ts int64, now time.Time, skew time.Duration) bool {
	// Comparisons avoid signed subtraction overflow on adversarial timestamps.
	return ts >= now.Add(-skew).Unix() && ts <= now.Add(skew).Unix()
}

type EncryptionService struct{ aead cipher.AEAD }

func NewEncryption(key string) (*EncryptionService, error) {
	b, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(b) != 32 {
		return nil, errors.New("ENCRYPTION_KEY must be base64-encoded 32 bytes")
	}
	block, err := aes.NewCipher(b)
	if err != nil {
		return nil, err
	}
	a, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &EncryptionService{a}, nil
}
func (e *EncryptionService) Encrypt(plain []byte, purpose string) ([]byte, error) {
	n := make([]byte, e.aead.NonceSize())
	if _, err := rand.Read(n); err != nil {
		return nil, err
	}
	return e.aead.Seal(n, n, plain, []byte(purpose)), nil
}
func (e *EncryptionService) Decrypt(data []byte, purpose string) ([]byte, error) {
	n := e.aead.NonceSize()
	if len(data) < n {
		return nil, errors.New("invalid ciphertext")
	}
	return e.aead.Open(nil, data[:n], data[n:], []byte(purpose))
}
