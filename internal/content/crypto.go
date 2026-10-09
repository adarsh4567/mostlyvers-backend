package content

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
)

func EncryptPackage(plain []byte) (ciphertext, key, nonce []byte, checksum string, err error) {
	key = make([]byte, 32)
	if _, err = io.ReadFull(rand.Reader, key); err != nil {
		return nil, nil, nil, "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, nil, "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, nil, "", err
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, nil, "", err
	}
	ciphertext = gcm.Seal(nil, nonce, plain, nil)
	sum := sha256.Sum256(ciphertext)
	checksum = fmt.Sprintf("%x", sum[:])
	return ciphertext, key, nonce, checksum, nil
}

func WrapKey(kek, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return append(nonce, gcm.Seal(nil, nonce, key, nil)...), nil
}
func UnwrapKey(kek, wrapped []byte) ([]byte, error) {
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(wrapped) < gcm.NonceSize() {
		return nil, errors.New("wrapped key is invalid")
	}
	return gcm.Open(nil, wrapped[:gcm.NonceSize()], wrapped[gcm.NonceSize():], nil)
}
func Base64(value []byte) string { return base64.StdEncoding.EncodeToString(value) }
