package media

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mostlyvers/backend/internal/config"
)

const prefix = "cloudinary:"

type Store struct {
	cloud, key, secret string
	client             *http.Client
}

func New(cfg config.Config) *Store {
	return &Store{cloud: cfg.CloudinaryCloudName, key: cfg.CloudinaryAPIKey, secret: cfg.CloudinaryAPISecret, client: &http.Client{Timeout: 45 * time.Second}}
}
func (s *Store) Configured() bool { return s.cloud != "" && s.key != "" && s.secret != "" }

func (s *Store) Upload(ctx context.Context, folder, filename string, content io.Reader) (string, error) {
	if !s.Configured() {
		return "", errors.New("Cloudinary is not configured")
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("api_key", s.key)
	_ = writer.WriteField("timestamp", timestamp)
	_ = writer.WriteField("folder", folder)
	_ = writer.WriteField("signature", sign("folder="+folder+"&timestamp="+timestamp, s.secret))
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return "", err
	}
	if _, err = io.Copy(part, content); err != nil {
		return "", err
	}
	if err = writer.Close(); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.cloudinary.com/v1_1/"+url.PathEscape(s.cloud)+"/image/upload", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	response, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	var result struct {
		PublicID  string `json:"public_id"`
		SecureURL string `json:"secure_url"`
		Error     *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err = json.NewDecoder(response.Body).Decode(&result); err != nil {
		return "", err
	}
	if response.StatusCode/100 != 2 || result.PublicID == "" || result.SecureURL == "" {
		message := fmt.Sprintf("Cloudinary upload returned %d", response.StatusCode)
		if result.Error != nil && result.Error.Message != "" {
			message = result.Error.Message
		}
		return "", errors.New(message)
	}
	return prefix + result.PublicID + "|" + result.SecureURL, nil
}

func (s *Store) Delete(ctx context.Context, reference string) error {
	publicID, _ := split(reference)
	if publicID == "" || !s.Configured() {
		return nil
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	values := url.Values{"api_key": {s.key}, "timestamp": {timestamp}, "public_id": {publicID}, "signature": {sign("public_id="+publicID+"&timestamp="+timestamp, s.secret)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.cloudinary.com/v1_1/"+url.PathEscape(s.cloud)+"/image/destroy", strings.NewReader(values.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("Cloudinary delete returned %d", response.StatusCode)
	}
	return nil
}

func URL(reference string) string       { _, value := split(reference); return value }
func IsReference(reference string) bool { return strings.HasPrefix(reference, prefix) }
func split(reference string) (string, string) {
	if !IsReference(reference) {
		return "", ""
	}
	parts := strings.SplitN(strings.TrimPrefix(reference, prefix), "|", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}
func sign(value, secret string) string {
	sum := sha1.Sum([]byte(value + secret))
	return hex.EncodeToString(sum[:])
}
