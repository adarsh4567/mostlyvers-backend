package integrity

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mostlyvers/backend/internal/domain"
)

type Verifier struct {
	ProjectNumber, PackageName string
	Required                   bool
	serviceAccount             []byte
	client                     *http.Client
}
type Result struct {
	RequestHash, Verdict string
	ExpiresAt            time.Time
}

func New(project, packageName string, required bool, serviceAccount []byte) *Verifier {
	return &Verifier{ProjectNumber: project, PackageName: packageName, Required: required, serviceAccount: serviceAccount, client: &http.Client{Timeout: 10 * time.Second}}
}
func RequestHash(action string, d domain.DeviceContext) string {
	sum := sha256.Sum256([]byte(action + "\n" + d.InstallationID + "\n" + d.PackageName + "\n" + d.AppVersion))
	return hex.EncodeToString(sum[:])
}
func (v *Verifier) Verify(ctx context.Context, action string, d domain.DeviceContext) (Result, error) {
	if !v.Required {
		return Result{RequestHash: RequestHash(action, d), Verdict: "DEVELOPMENT_BYPASS", ExpiresAt: time.Now().Add(2 * time.Minute)}, nil
	}
	if d.IntegrityToken == "" || d.IntegrityRequestHash == "" {
		return Result{}, errors.New("integrity token is required")
	}
	expected := RequestHash(action, d)
	if d.IntegrityRequestHash != expected {
		return Result{}, errors.New("integrity request hash mismatch")
	}
	token, err := v.accessToken(ctx)
	if err != nil {
		return Result{}, err
	}
	endpoint := "https://playintegrity.googleapis.com/v1/" + url.PathEscape(v.PackageName) + ":decodeIntegrityToken"
	payload, _ := json.Marshal(map[string]string{"integrity_token": d.IntegrityToken})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	response, err := v.client.Do(req)
	if err != nil {
		return Result{}, err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return Result{}, fmt.Errorf("Play Integrity decode returned %d", response.StatusCode)
	}
	var decoded struct {
		TokenPayloadExternal struct {
			RequestDetails  struct{ RequestPackageName, RequestHash, TimestampMillis string }
			AppIntegrity    struct{ AppRecognitionVerdict, PackageName string }
			DeviceIntegrity struct{ DeviceRecognitionVerdict []string }
		} `json:"tokenPayloadExternal"`
	}
	if json.NewDecoder(response.Body).Decode(&decoded) != nil {
		return Result{}, errors.New("invalid Play Integrity response")
	}
	p := decoded.TokenPayloadExternal
	millis, err := strconv.ParseInt(p.RequestDetails.TimestampMillis, 10, 64)
	if err != nil {
		return Result{}, errors.New("invalid integrity timestamp")
	}
	issued := time.UnixMilli(millis)
	if time.Since(issued) > 2*time.Minute || issued.After(time.Now().Add(30*time.Second)) {
		return Result{}, errors.New("stale integrity verdict")
	}
	if p.RequestDetails.RequestPackageName != v.PackageName || p.AppIntegrity.PackageName != v.PackageName || p.RequestDetails.RequestHash != expected || p.AppIntegrity.AppRecognitionVerdict != "PLAY_RECOGNIZED" || !contains(p.DeviceIntegrity.DeviceRecognitionVerdict, "MEETS_DEVICE_INTEGRITY") {
		return Result{}, errors.New("device integrity verification failed")
	}
	return Result{RequestHash: expected, Verdict: "MEETS_DEVICE_INTEGRITY", ExpiresAt: issued.Add(2 * time.Minute)}, nil
}
func (v *Verifier) metadataToken(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Metadata-Flavor", "Google")
	response, err := v.client.Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return "", fmt.Errorf("metadata token returned %d", response.StatusCode)
	}
	var value struct {
		AccessToken string `json:"access_token"`
	}
	if json.NewDecoder(response.Body).Decode(&value) != nil || value.AccessToken == "" {
		return "", errors.New("metadata access token unavailable")
	}
	return value.AccessToken, nil
}

func (v *Verifier) accessToken(ctx context.Context) (string, error) {
	if len(v.serviceAccount) == 0 {
		return v.metadataToken(ctx)
	}
	var credentials struct {
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
		TokenURI    string `json:"token_uri"`
	}
	if err := json.Unmarshal(v.serviceAccount, &credentials); err != nil {
		return "", errors.New("invalid Google service account JSON")
	}
	if credentials.ClientEmail == "" || credentials.PrivateKey == "" {
		return "", errors.New("Google service account credentials are incomplete")
	}
	if credentials.TokenURI == "" {
		credentials.TokenURI = "https://oauth2.googleapis.com/token"
	}
	block, _ := pem.Decode([]byte(credentials.PrivateKey))
	if block == nil {
		return "", errors.New("invalid Google service account private key")
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", err
	}
	privateKey, ok := key.(*rsa.PrivateKey)
	if !ok {
		return "", errors.New("Google service account key is not RSA")
	}
	now := time.Now().Unix()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claimsJSON, _ := json.Marshal(map[string]any{"iss": credentials.ClientEmail, "scope": "https://www.googleapis.com/auth/playintegrity", "aud": credentials.TokenURI, "iat": now, "exp": now + 3600})
	claims := base64.RawURLEncoding.EncodeToString(claimsJSON)
	unsigned := header + "." + claims
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	assertion := unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {assertion}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, credentials.TokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := v.client.Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	var result struct {
		AccessToken string `json:"access_token"`
	}
	if response.StatusCode/100 != 2 || json.NewDecoder(response.Body).Decode(&result) != nil || result.AccessToken == "" {
		return "", fmt.Errorf("Google OAuth token returned %d", response.StatusCode)
	}
	return result.AccessToken, nil
}
func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
