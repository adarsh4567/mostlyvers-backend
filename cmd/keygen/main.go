package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log"
)

func main() {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	contentKey := randomBytes(32)
	jobToken := randomBytes(32)
	backupPassphrase := randomBytes(32)

	fmt.Printf("ACCESS_TOKEN_PRIVATE_KEY_BASE64=%s\n", base64.StdEncoding.EncodeToString(privateKey))
	fmt.Printf("CONTENT_KEY_ENCRYPTION_KEY_BASE64=%s\n", base64.StdEncoding.EncodeToString(contentKey))
	fmt.Printf("INTERNAL_JOB_TOKEN=%s\n", base64.RawURLEncoding.EncodeToString(jobToken))
	fmt.Printf("BACKUP_PASSPHRASE=%s\n", base64.RawURLEncoding.EncodeToString(backupPassphrase))
}

func randomBytes(size int) []byte {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		log.Fatal(err)
	}
	return value
}
