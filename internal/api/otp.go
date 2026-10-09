package api

import (
	"crypto/rand"
	"crypto/subtle"
	"fmt"
	"html"
	"math/big"
	"net/http"
	"strings"
	"time"

	emailpkg "github.com/mostlyvers/backend/internal/email"
	"github.com/mostlyvers/backend/internal/httpx"
	"github.com/mostlyvers/backend/internal/store"
)

func otpCode() (string, error) {
	value, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", value.Int64()), nil
}

func (s *Server) requestSignupOTP(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email string `json:"email"`
	}
	if !httpx.Decode(w, r, &input) {
		return
	}
	email := strings.TrimSpace(input.Email)
	if !validateEmail(email) {
		httpx.WriteError(w, r, httpx.NewError(422, "VALIDATION_ERROR", "Enter a valid email address."))
		return
	}
	if !s.allowRate(r.Context(), "signup-otp:"+clientKey(r)+":"+store.NormalizeEmail(email), 5, time.Hour) {
		httpx.WriteError(w, r, httpx.NewError(429, "RATE_LIMITED", "Too many codes requested. Try again later."))
		return
	}
	if _, err := s.store.FindAccount(r.Context(), "READER", email); err == nil {
		httpx.WriteError(w, r, httpx.NewError(409, "EMAIL_EXISTS", "An account already exists for this email."))
		return
	}
	id, code := store.NewID(), ""
	var err error
	if code, err = otpCode(); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_, err = s.store.Pool.Exec(r.Context(), `INSERT INTO email_otp_challenges(id,normalized_email,code_hash,purpose,expires_at) VALUES($1,$2,$3,'SIGNUP',now()+interval '10 minutes')`, id, store.NormalizeEmail(email), store.HashText(id+":"+code))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	err = s.email.Send(r.Context(), emailpkg.Message{To: email, Subject: "Your MOSTLYVERS verification code", HTML: `<p>Your verification code is <strong>` + html.EscapeString(code) + `</strong>.</p><p>It expires in 10 minutes.</p>`})
	if err != nil {
		httpx.WriteError(w, r, httpx.NewError(503, "EMAIL_UNAVAILABLE", "The verification email could not be sent. Try again."))
		return
	}
	response := map[string]any{"challengeId": id, "expiresAt": time.Now().UTC().Add(10 * time.Minute)}
	if s.cfg.Environment != "production" {
		response["developmentCode"] = code
	}
	httpx.JSON(w, 202, response)
}

func (s *Server) consumeSignupOTP(r *http.Request, challengeID, email, code string) error {
	if !s.cfg.OTPRequired && challengeID == "" && code == "" {
		return nil
	}
	var expected []byte
	var attempts int
	err := s.store.Pool.QueryRow(r.Context(), `SELECT code_hash,attempts FROM email_otp_challenges WHERE id=$1 AND normalized_email=$2 AND purpose='SIGNUP' AND consumed_at IS NULL AND expires_at>now()`, challengeID, store.NormalizeEmail(email)).Scan(&expected, &attempts)
	if err != nil || attempts >= 5 {
		return httpx.NewError(422, "OTP_INVALID", "The verification code is invalid or expired.")
	}
	actual := store.HashText(challengeID + ":" + strings.TrimSpace(code))
	if len(actual) != len(expected) || subtle.ConstantTimeCompare(actual, expected) != 1 {
		_, _ = s.store.Pool.Exec(r.Context(), `UPDATE email_otp_challenges SET attempts=attempts+1 WHERE id=$1`, challengeID)
		return httpx.NewError(422, "OTP_INVALID", "The verification code is invalid or expired.")
	}
	tag, err := s.store.Pool.Exec(r.Context(), `UPDATE email_otp_challenges SET consumed_at=now() WHERE id=$1 AND consumed_at IS NULL`, challengeID)
	if err != nil || tag.RowsAffected() != 1 {
		return httpx.NewError(422, "OTP_INVALID", "The verification code is invalid or expired.")
	}
	return nil
}
