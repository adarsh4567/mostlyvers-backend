package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/mostlyvers/backend/internal/auth"
	"github.com/mostlyvers/backend/internal/domain"
	emailpkg "github.com/mostlyvers/backend/internal/email"
	"github.com/mostlyvers/backend/internal/httpx"
	"github.com/mostlyvers/backend/internal/store"
)

type loginRequest struct {
	Email      string               `json:"email"`
	Password   string               `json:"password"`
	RememberMe bool                 `json:"rememberMe"`
	Device     domain.DeviceContext `json:"device"`
}

func validateEmail(value string) bool {
	parsed, err := mail.ParseAddress(value)
	return err == nil && strings.EqualFold(parsed.Address, strings.TrimSpace(value))
}
func validateDevice(d domain.DeviceContext) error {
	if d.InstallationID == "" || d.DisplayName == "" || d.Platform != "ANDROID" || d.PackageName == "" {
		return errors.New("complete Android device context is required")
	}
	return nil
}

func (s *Server) readerSignup(w http.ResponseWriter, r *http.Request) {
	if !s.allowRate(r.Context(), "signup:"+clientKey(r), 10, 15*time.Minute) {
		httpx.WriteError(w, r, httpx.NewError(429, "RATE_LIMITED", "Too many attempts. Try again later."))
		return
	}
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		httpx.WriteError(w, r, httpx.NewError(422, "VALIDATION_ERROR", "Signup form is invalid."))
		return
	}
	age := 0
	fmt.Sscanf(r.FormValue("age"), "%d", &age)
	var device domain.DeviceContext
	if json.Unmarshal([]byte(r.FormValue("device")), &device) != nil || validateDevice(device) != nil {
		httpx.WriteError(w, r, httpx.NewError(422, "VALIDATION_ERROR", "Valid device information is required."))
		return
	}
	verdict, err := s.integrity.Verify(r.Context(), "SIGNUP", device)
	if err != nil {
		httpx.WriteError(w, r, httpx.NewError(403, "INTEGRITY_VERIFICATION_FAILED", "This Android installation could not be verified."))
		return
	}
	email := strings.TrimSpace(r.FormValue("email"))
	password := r.FormValue("password")
	if !validateEmail(email) || age < 13 || strings.TrimSpace(r.FormValue("name")) == "" || strings.TrimSpace(r.FormValue("gender")) == "" || strings.TrimSpace(r.FormValue("phone")) == "" {
		httpx.WriteError(w, r, httpx.NewError(422, "VALIDATION_ERROR", "Name, age, email and phone are required."))
		return
	}
	if err := s.consumeSignupOTP(r, r.FormValue("otpChallengeId"), email, r.FormValue("otp")); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		httpx.WriteError(w, r, httpx.NewError(422, "WEAK_PASSWORD", err.Error()))
		return
	}
	account, deviceID, err := s.store.CreateReader(r.Context(), store.Signup{Email: email, PasswordHash: hash, Name: r.FormValue("name"), Age: age, Gender: r.FormValue("gender"), Phone: r.FormValue("phone"), Device: device})
	if store.IsUniqueViolation(err) {
		httpx.WriteError(w, r, httpx.NewError(409, "EMAIL_EXISTS", "An account already exists for this email."))
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	s.recordIntegrity(r, account.ID, deviceID, verdict)
	if file, header, fileErr := r.FormFile("profilePicture"); fileErr == nil {
		defer file.Close()
		contentType := header.Header.Get("Content-Type")
		if header.Size <= 5<<20 && (contentType == "image/jpeg" || contentType == "image/png" || contentType == "image/webp") {
			key, uploadErr := s.storeImage(r.Context(), "profiles/readers/"+account.ID, header.Filename, contentType, file, header.Size)
			if uploadErr == nil {
				_, _ = s.store.Pool.Exec(r.Context(), `UPDATE reader_profiles SET profile_object_key=$1 WHERE account_id=$2`, key, account.ID)
			}
		}
	}
	session, err := s.newReaderSession(r, account, deviceID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, 201, session)
}
func (s *Server) readerLogin(w http.ResponseWriter, r *http.Request) {
	var input loginRequest
	if !httpx.Decode(w, r, &input) {
		return
	}
	if !s.allowRate(r.Context(), "login:"+clientKey(r)+":"+store.NormalizeEmail(input.Email), 10, 15*time.Minute) {
		httpx.WriteError(w, r, httpx.NewError(429, "RATE_LIMITED", "Too many attempts. Try again later."))
		return
	}
	if validateDevice(input.Device) != nil {
		httpx.WriteError(w, r, httpx.NewError(422, "VALIDATION_ERROR", "Valid device information is required."))
		return
	}
	verdict, verifyErr := s.integrity.Verify(r.Context(), "LOGIN", input.Device)
	if verifyErr != nil {
		httpx.WriteError(w, r, httpx.NewError(403, "INTEGRITY_VERIFICATION_FAILED", "This Android installation could not be verified."))
		return
	}
	account, err := s.store.FindAccount(r.Context(), "READER", input.Email)
	if err != nil || account.Status != "ACTIVE" || !auth.VerifyPassword(account.PasswordHash, input.Password) {
		httpx.WriteError(w, r, httpx.NewError(401, "INVALID_CREDENTIALS", "Email or password is incorrect."))
		return
	}
	deviceID, err := s.store.UpsertLoginDevice(r.Context(), account.ID, input.Device)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	s.recordIntegrity(r, account.ID, deviceID, verdict)
	session, err := s.newReaderSession(r, account, deviceID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, 200, session)
}
func (s *Server) newReaderSession(r *http.Request, account store.Account, deviceID string) (domain.ReaderSession, error) {
	plain, hash, err := auth.OpaqueToken()
	if err != nil {
		return domain.ReaderSession{}, err
	}
	record, err := s.store.CreateRefreshSession(r.Context(), account.ID, "READER", "ANDROID", deviceID, hash, time.Now().Add(s.cfg.RefreshTTL), "")
	if err != nil {
		return domain.ReaderSession{}, err
	}
	access, expiry, err := s.tokens.Issue(account.ID, "READER", record.ID)
	if err != nil {
		return domain.ReaderSession{}, err
	}
	profile, err := s.store.ReaderProfile(r.Context(), account.ID)
	if err != nil {
		return domain.ReaderSession{}, err
	}
	s.signReaderPicture(r.Context(), profile)
	deviceAccess, err := s.store.DeviceAccess(r.Context(), account.ID, deviceID)
	if err != nil {
		return domain.ReaderSession{}, err
	}
	return domain.ReaderSession{AccessToken: access, AccessTokenExpiresAt: expiry, RefreshToken: plain, Reader: profile, AccessMode: deviceAccess.AccessMode, DeviceAccess: deviceAccess}, nil
}
func (s *Server) readerRefresh(w http.ResponseWriter, r *http.Request) {
	var input struct {
		RefreshToken string `json:"refreshToken"`
	}
	if !httpx.Decode(w, r, &input) {
		return
	}
	session, err := s.rotate(r, input.RefreshToken, "ANDROID")
	if err != nil {
		httpx.WriteError(w, r, httpx.NewError(401, "AUTH_EXPIRED", "The refresh session has expired."))
		return
	}
	account, _ := s.store.AccountByID(r.Context(), session.principal.AccountID)
	result, err := s.readerSessionFromRotation(r, account, session.deviceID, session)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, 200, result)
}

type rotated struct {
	principal               domain.Principal
	plain, access, deviceID string
	expiry                  time.Time
}

func (s *Server) rotate(r *http.Request, plain, client string) (rotated, error) {
	newPlain, newHash, err := auth.OpaqueToken()
	if err != nil {
		return rotated{}, err
	}
	record, err := s.store.RotateRefreshSession(r.Context(), auth.TokenHash(plain), newHash, time.Now().Add(s.cfg.RefreshTTL))
	if err != nil || record.ClientType != client {
		return rotated{}, errors.New("invalid refresh")
	}
	access, expiry, err := s.tokens.Issue(record.AccountID, record.Role, record.ID)
	return rotated{principal: domain.Principal{AccountID: record.AccountID, Role: record.Role, SessionID: record.ID}, plain: newPlain, access: access, deviceID: record.DeviceID, expiry: expiry}, err
}
func (s *Server) readerSessionFromRotation(r *http.Request, a store.Account, deviceID string, x rotated) (domain.ReaderSession, error) {
	profile, err := s.store.ReaderProfile(r.Context(), a.ID)
	if err != nil {
		return domain.ReaderSession{}, err
	}
	s.signReaderPicture(r.Context(), profile)
	access, err := s.store.DeviceAccess(r.Context(), a.ID, deviceID)
	if err != nil {
		return domain.ReaderSession{}, err
	}
	return domain.ReaderSession{AccessToken: x.access, AccessTokenExpiresAt: x.expiry, RefreshToken: x.plain, Reader: profile, AccessMode: access.AccessMode, DeviceAccess: access}, nil
}
func (s *Server) readerLogout(w http.ResponseWriter, r *http.Request) {
	token := httpx.Bearer(r)
	if p, err := s.tokens.Verify(token); err == nil {
		_ = s.store.RevokeSession(r.Context(), p.SessionID)
	}
	w.WriteHeader(204)
}

func (s *Server) adminLogin(w http.ResponseWriter, r *http.Request) {
	var input loginRequest
	if !httpx.Decode(w, r, &input) {
		return
	}
	account, err := s.store.FindAccount(r.Context(), "OWNER", input.Email)
	if err != nil || account.Status != "ACTIVE" || !auth.VerifyPassword(account.PasswordHash, input.Password) {
		httpx.WriteError(w, r, httpx.NewError(401, "INVALID_CREDENTIALS", "Email or password is incorrect."))
		return
	}
	plain, hash, err := auth.OpaqueToken()
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	ttl := s.cfg.RefreshTTL
	if !input.RememberMe && ttl > 24*time.Hour {
		ttl = 24 * time.Hour
	}
	record, err := s.store.CreateRefreshSession(r.Context(), account.ID, "OWNER", "ADMIN", "", hash, time.Now().Add(ttl), "")
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	access, expiry, _ := s.tokens.Issue(account.ID, "OWNER", record.ID)
	csrf, _, _ := auth.OpaqueToken()
	s.setAdminCookies(w, plain, csrf, ttl)
	owner, err := s.store.OwnerProfile(r.Context(), account.ID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	s.signOwnerPicture(r.Context(), &owner)
	httpx.JSON(w, 200, domain.AdminSession{AccessToken: access, AccessTokenExpiresAt: expiry, CSRFToken: csrf, Owner: owner, Capabilities: []string{"*"}})
}
func (s *Server) setAdminCookies(w http.ResponseWriter, refresh, csrf string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{Name: "mv_refresh", Value: refresh, Path: "/v1/admin/auth", HttpOnly: true, Secure: s.cfg.AdminCookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: int(ttl.Seconds())})
	http.SetCookie(w, &http.Cookie{Name: "mv_csrf", Value: csrf, Path: "/v1", HttpOnly: false, Secure: s.cfg.AdminCookieSecure, SameSite: http.SameSiteLaxMode, MaxAge: int(ttl.Seconds())})
}
func (s *Server) adminRefresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("mv_refresh")
	if err != nil {
		httpx.WriteError(w, r, httpx.NewError(401, "AUTH_EXPIRED", "The refresh session has expired."))
		return
	}
	x, err := s.rotate(r, cookie.Value, "ADMIN")
	if err != nil {
		httpx.WriteError(w, r, httpx.NewError(401, "AUTH_EXPIRED", "The refresh session has expired."))
		return
	}
	csrf, _, _ := auth.OpaqueToken()
	s.setAdminCookies(w, x.plain, csrf, s.cfg.RefreshTTL)
	owner, err := s.store.OwnerProfile(r.Context(), x.principal.AccountID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	s.signOwnerPicture(r.Context(), &owner)
	httpx.JSON(w, 200, domain.AdminSession{AccessToken: x.access, AccessTokenExpiresAt: x.expiry, CSRFToken: csrf, Owner: owner, Capabilities: []string{"*"}})
}
func (s *Server) adminLogout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie("mv_refresh"); err == nil {
		var id string
		_ = s.store.Pool.QueryRow(r.Context(), `SELECT id FROM auth_sessions WHERE token_hash=$1`, auth.TokenHash(cookie.Value)).Scan(&id)
		if id != "" {
			_ = s.store.RevokeSession(r.Context(), id)
		}
	}
	s.setAdminCookies(w, "", "", -time.Hour)
	w.WriteHeader(204)
}

func (s *Server) passwordResetRequest(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email string `json:"email"`
	}
	if !httpx.Decode(w, r, &input) {
		return
	}
	if !s.allowRate(r.Context(), "reset:"+clientKey(r)+":"+store.NormalizeEmail(input.Email), 5, time.Hour) {
		httpx.JSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
		return
	}
	role := "READER"
	if strings.Contains(r.URL.Path, "/admin/") {
		role = "OWNER"
	}
	if account, err := s.store.FindAccount(r.Context(), role, input.Email); err == nil {
		plain, hash, _ := auth.OpaqueToken()
		_, _ = s.store.Pool.Exec(r.Context(), `INSERT INTO one_time_tokens(id,account_id,purpose,token_hash,expires_at) VALUES($1,$2,'PASSWORD_RESET',$3,$4)`, store.NewID(), account.ID, hash, time.Now().Add(time.Hour))
		link := s.cfg.ReaderResetURL + "/" + plain
		if role == "OWNER" {
			link = cleanURL(s.cfg.AdminOrigin) + "/forgot-password?token=" + plain
		}
		_ = s.email.Send(r.Context(), emailpkg.Message{To: account.Email, Subject: "Reset your MOSTLYVERS password", HTML: `<p>Use this secure link within one hour:</p><p><a href="` + html.EscapeString(link) + `">Reset password</a></p>`})
	}
	httpx.JSON(w, 202, map[string]bool{"accepted": true})
}
func (s *Server) passwordResetConfirm(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !httpx.Decode(w, r, &input) {
		return
	}
	hash, err := auth.HashPassword(input.Password)
	if err != nil {
		httpx.WriteError(w, r, httpx.NewError(422, "WEAK_PASSWORD", err.Error()))
		return
	}
	tx, err := s.store.Pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var accountID string
	err = tx.QueryRow(r.Context(), `UPDATE one_time_tokens SET used_at=now() WHERE token_hash=$1 AND purpose='PASSWORD_RESET' AND used_at IS NULL AND expires_at>now() RETURNING account_id`, auth.TokenHash(input.Token)).Scan(&accountID)
	if err != nil {
		httpx.WriteError(w, r, httpx.NewError(400, "RESET_TOKEN_INVALID", "The reset link is invalid or expired."))
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE accounts SET password_hash=$1,updated_at=now() WHERE id=$2`, hash, accountID)
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE auth_sessions SET revoked_at=now() WHERE account_id=$1 AND revoked_at IS NULL`, accountID)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(204)
}
