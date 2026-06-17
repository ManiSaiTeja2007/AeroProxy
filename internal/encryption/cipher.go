package encryption

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"sync"
)

var aeadCache sync.Map // map[string]cipher.AEAD

// GetKey retrieves the 32-byte encryption key from the ENCRYPTION_KEY environment variable.
// If it is empty or not 32 bytes, it falls back to the default MVP key.
func GetKey() []byte {
	k := os.Getenv("ENCRYPTION_KEY")
	if len(k) == 32 {
		return []byte(k)
	}
	return []byte("0123456789abcdef0123456789abcdef")
}

// getAEAD retrieves a cached GCM instance for the key, or creates one if it doesn't exist.
func getAEAD(key []byte) (cipher.AEAD, error) {
	keyStr := string(key)
	if val, ok := aeadCache.Load(keyStr); ok {
		return val.(cipher.AEAD), nil
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	aeadCache.Store(keyStr, gcm)
	return gcm, nil
}

// Encrypt encrypts the plainText string using AES-GCM and the provided 32-byte key.
// It returns a base64-encoded string containing both the nonce and ciphertext.
func Encrypt(plainText string, key []byte) (string, error) {
	if len(key) != 32 {
		return "", errors.New("key must be exactly 32 bytes")
	}

	gcm, err := getAEAD(key)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	cipherText := gcm.Seal(nonce, nonce, []byte(plainText), nil)
	return base64.StdEncoding.EncodeToString(cipherText), nil
}
