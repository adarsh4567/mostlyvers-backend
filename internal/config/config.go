package config

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Environment              string
	Port                     string
	PublicBaseURL            string
	AdminOrigin              string
	DatabaseURL              string
	AccessPrivateKey         ed25519.PrivateKey
	AccessPublicKey          ed25519.PublicKey
	AccessTTL                time.Duration
	RefreshTTL               time.Duration
	AdminCookieSecure        bool
	R2Endpoint               string
	R2PresignEndpoint        string
	R2Region                 string
	R2Bucket                 string
	R2PublicBucket           string
	R2AccessKey              string
	R2SecretKey              string
	R2PublicBaseURL          string
	ContentKEK               []byte
	ResendAPIKey             string
	EmailFrom                string
	EmailProvider            string
	SMTPAddr                 string
	IntegrityRequired        bool
	GoogleProjectNumber      string
	GoogleServiceAccountJSON []byte
	AndroidPackageName       string
	InternalJobToken         string
	StorageSoftLimit         int64
	StorageHardLimit         int64
	BillingMode              string
	CloudinaryCloudName      string
	CloudinaryAPIKey         string
	CloudinaryAPISecret      string
	ExpoPushEndpoint         string
	OTPRequired              bool
	ReaderResetURL           string
}

func Load() (Config, error) {
	accessTTL, err := time.ParseDuration(env("ACCESS_TOKEN_TTL", "15m"))
	if err != nil {
		return Config{}, fmt.Errorf("ACCESS_TOKEN_TTL: %w", err)
	}
	refreshTTL, err := time.ParseDuration(env("REFRESH_TOKEN_TTL", "720h"))
	if err != nil {
		return Config{}, fmt.Errorf("REFRESH_TOKEN_TTL: %w", err)
	}
	privateKey, publicKey, err := loadSigningKeys()
	if err != nil {
		return Config{}, err
	}
	serviceAccountJSON, err := decodeOptional("GOOGLE_SERVICE_ACCOUNT_JSON_BASE64")
	if err != nil {
		return Config{}, err
	}
	kek, err := decodeOrRandom("CONTENT_KEY_ENCRYPTION_KEY_BASE64", 32)
	if err != nil {
		return Config{}, err
	}
	soft, err := strconv.ParseInt(env("STORAGE_SOFT_LIMIT_BYTES", "8589934592"), 10, 64)
	if err != nil {
		return Config{}, err
	}
	hard, err := strconv.ParseInt(env("STORAGE_HARD_LIMIT_BYTES", "10200547328"), 10, 64)
	if err != nil {
		return Config{}, err
	}
	c := Config{
		Environment: env("APP_ENV", "development"), Port: env("HTTP_PORT", env("PORT", "5001")), PublicBaseURL: strings.TrimRight(env("PUBLIC_BASE_URL", "http://localhost:5001"), "/"),
		AdminOrigin: strings.TrimRight(env("ADMIN_ORIGIN", "http://localhost:5174"), "/"), DatabaseURL: os.Getenv("DATABASE_URL"), AccessPrivateKey: privateKey, AccessPublicKey: publicKey,
		AccessTTL: accessTTL, RefreshTTL: refreshTTL, AdminCookieSecure: boolEnv("ADMIN_COOKIE_SECURE", false), R2Endpoint: os.Getenv("R2_ENDPOINT"), R2PresignEndpoint: os.Getenv("R2_PRESIGN_ENDPOINT"), R2Region: env("R2_REGION", "auto"),
		R2Bucket: env("R2_BUCKET", "mostlyvers-private"), R2PublicBucket: env("R2_PUBLIC_BUCKET", "mostlyvers-public"), R2AccessKey: os.Getenv("R2_ACCESS_KEY_ID"), R2SecretKey: os.Getenv("R2_SECRET_ACCESS_KEY"),
		R2PublicBaseURL: strings.TrimRight(os.Getenv("R2_PUBLIC_BASE_URL"), "/"), ContentKEK: kek, ResendAPIKey: os.Getenv("RESEND_API_KEY"), EmailFrom: env("EMAIL_FROM", "MOSTLYVERS <hello@example.test>"),
		EmailProvider: env("EMAIL_PROVIDER", "console"), SMTPAddr: env("SMTP_ADDR", "localhost:1025"), IntegrityRequired: boolEnv("PLAY_INTEGRITY_REQUIRED", false), GoogleProjectNumber: os.Getenv("GOOGLE_CLOUD_PROJECT_NUMBER"), GoogleServiceAccountJSON: serviceAccountJSON, AndroidPackageName: env("ANDROID_PACKAGE_NAME", "com.mostlyvers.app"),
		InternalJobToken: os.Getenv("INTERNAL_JOB_TOKEN"), StorageSoftLimit: soft, StorageHardLimit: hard,
		BillingMode: strings.ToUpper(env("BILLING_MODE", "DISABLED")), CloudinaryCloudName: os.Getenv("CLOUDINARY_CLOUD_NAME"), CloudinaryAPIKey: os.Getenv("CLOUDINARY_API_KEY"), CloudinaryAPISecret: os.Getenv("CLOUDINARY_API_SECRET"),
		ExpoPushEndpoint: env("EXPO_PUSH_ENDPOINT", "https://exp.host/--/api/v2/push/send"), OTPRequired: boolEnv("EMAIL_OTP_REQUIRED", os.Getenv("APP_ENV") == "production"), ReaderResetURL: strings.TrimRight(env("READER_RESET_URL", "mostlyvers://reset-password"), "/"),
	}
	if c.R2PresignEndpoint == "" {
		if c.Environment == "development" {
			c.R2PresignEndpoint = "http://localhost:9000"
		} else {
			c.R2PresignEndpoint = c.R2Endpoint
		}
	}
	if c.DatabaseURL == "" {
		return Config{}, errors.New("DATABASE_URL is required")
	}
	for name, value := range map[string]string{
		"PUBLIC_BASE_URL":     c.PublicBaseURL,
		"ADMIN_ORIGIN":        c.AdminOrigin,
		"R2_ENDPOINT":         c.R2Endpoint,
		"R2_PRESIGN_ENDPOINT": c.R2PresignEndpoint,
		"R2_PUBLIC_BASE_URL":  c.R2PublicBaseURL,
	} {
		if value != "" {
			if err := validateHTTPURL(name, value); err != nil {
				return Config{}, err
			}
		}
	}
	if c.Environment == "production" {
		if c.InternalJobToken == "" || len(c.InternalJobToken) < 24 {
			return Config{}, errors.New("a strong INTERNAL_JOB_TOKEN is required in production")
		}
		if c.R2Endpoint == "" || c.R2AccessKey == "" || c.R2SecretKey == "" {
			return Config{}, errors.New("R2 configuration is required in production")
		}
		if os.Getenv("ACCESS_TOKEN_PRIVATE_KEY_BASE64") == "" {
			return Config{}, errors.New("explicit access-token keys are required in production")
		}
		if os.Getenv("CONTENT_KEY_ENCRYPTION_KEY_BASE64") == "" {
			return Config{}, errors.New("explicit content encryption key is required in production")
		}
		if !c.AdminCookieSecure || !strings.HasPrefix(c.AdminOrigin, "https://") {
			return Config{}, errors.New("secure admin cookies and an HTTPS admin origin are required in production")
		}
		if c.EmailProvider != "resend" || c.ResendAPIKey == "" {
			return Config{}, errors.New("Resend email configuration is required in production")
		}
		if c.CloudinaryCloudName == "" || c.CloudinaryAPIKey == "" || c.CloudinaryAPISecret == "" {
			return Config{}, errors.New("Cloudinary image configuration is required in production")
		}
		if c.IntegrityRequired && c.GoogleProjectNumber == "" {
			return Config{}, errors.New("GOOGLE_CLOUD_PROJECT_NUMBER is required when Play Integrity is enforced")
		}
		if c.IntegrityRequired && len(c.GoogleServiceAccountJSON) == 0 {
			return Config{}, errors.New("GOOGLE_SERVICE_ACCOUNT_JSON_BASE64 is required for Play Integrity on Render")
		}
	}
	if c.BillingMode != "DISABLED" && c.BillingMode != "FREE_LAUNCH" {
		return Config{}, errors.New("BILLING_MODE must be DISABLED or FREE_LAUNCH")
	}
	return c, nil
}

func validateHTTPURL(name, value string) error {
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return fmt.Errorf("%s must be an absolute http(s) URL, got %q", name, value)
	}
	return nil
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
func boolEnv(name string, fallback bool) bool {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	return err == nil && parsed
}

func loadSigningKeys() (ed25519.PrivateKey, ed25519.PublicKey, error) {
	privateText, publicText := os.Getenv("ACCESS_TOKEN_PRIVATE_KEY_BASE64"), os.Getenv("ACCESS_TOKEN_PUBLIC_KEY_BASE64")
	if privateText == "" {
		public, private, err := ed25519.GenerateKey(rand.Reader)
		return private, public, err
	}
	privateBytes, err := base64.StdEncoding.DecodeString(privateText)
	if err != nil {
		return nil, nil, fmt.Errorf("decode private key: %w", err)
	}
	if len(privateBytes) != ed25519.PrivateKeySize {
		return nil, nil, errors.New("ACCESS_TOKEN_PRIVATE_KEY_BASE64 must contain a 64-byte Ed25519 key")
	}
	private := ed25519.PrivateKey(privateBytes)
	public := private.Public().(ed25519.PublicKey)
	if publicText != "" {
		bytes, err := base64.StdEncoding.DecodeString(publicText)
		if err != nil {
			return nil, nil, err
		}
		if len(bytes) != ed25519.PublicKeySize || !public.Equal(ed25519.PublicKey(bytes)) {
			return nil, nil, errors.New("access-token public key does not match private key")
		}
	}
	return private, public, nil
}

func decodeOrRandom(name string, size int) ([]byte, error) {
	text := os.Getenv(name)
	if text == "" {
		value := make([]byte, size)
		_, err := rand.Read(value)
		return value, err
	}
	value, err := base64.StdEncoding.DecodeString(text)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if len(value) != size {
		return nil, fmt.Errorf("%s must decode to %d bytes", name, size)
	}
	return value, nil
}

func decodeOptional(name string) ([]byte, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return nil, nil
	}
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return decoded, nil
}
