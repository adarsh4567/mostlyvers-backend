package api

import (
	"context"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/mostlyvers/backend/internal/auth"
	"github.com/mostlyvers/backend/internal/config"
	"github.com/mostlyvers/backend/internal/domain"
	mail "github.com/mostlyvers/backend/internal/email"
	"github.com/mostlyvers/backend/internal/httpx"
	"github.com/mostlyvers/backend/internal/integrity"
	"github.com/mostlyvers/backend/internal/media"
	"github.com/mostlyvers/backend/internal/objectstore"
	"github.com/mostlyvers/backend/internal/store"
)

type Server struct {
	cfg       config.Config
	store     *store.Store
	tokens    *auth.Tokens
	objects   *objectstore.Store
	email     mail.Sender
	payments  domain.PaymentProvider
	integrity *integrity.Verifier
	media     *media.Store
	started   time.Time
}

func New(cfg config.Config, database *store.Store, objects *objectstore.Store, sender mail.Sender, payments domain.PaymentProvider) *Server {
	return &Server{cfg: cfg, store: database, tokens: auth.NewTokens(cfg.AccessPrivateKey, cfg.AccessPublicKey, cfg.AccessTTL), objects: objects, email: sender, payments: payments, integrity: integrity.New(cfg.GoogleProjectNumber, cfg.AndroidPackageName, cfg.IntegrityRequired, cfg.GoogleServiceAccountJSON), media: media.New(cfg), started: time.Now().UTC()}
}

func (s *Server) imageURL(reference string) string {
	if media.IsReference(reference) {
		return media.URL(reference)
	}
	return s.objects.PublicURL(reference)
}

func (s *Server) deleteImage(ctx context.Context, reference string) {
	if media.IsReference(reference) {
		_ = s.media.Delete(ctx, reference)
		return
	}
	_ = s.objects.Delete(ctx, s.objects.PublicBucket, reference)
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RealIP, s.requestID, s.recoverer, s.securityHeaders, s.cors, middleware.Compress(5), s.timeout)
	r.Get("/health/live", func(w http.ResponseWriter, r *http.Request) {
		httpx.JSON(w, 200, map[string]any{"status": "ok", "uptimeSeconds": int(time.Since(s.started).Seconds())})
	})
	r.Get("/health/ready", func(w http.ResponseWriter, r *http.Request) {
		if err := s.store.Ping(r.Context()); err != nil {
			httpx.WriteError(w, r, httpx.NewError(503, "DATABASE_UNAVAILABLE", "The database is unavailable."))
			return
		}
		httpx.JSON(w, 200, map[string]string{"status": "ready"})
	})
	r.Get("/version", func(w http.ResponseWriter, r *http.Request) {
		info, _ := debug.ReadBuildInfo()
		version := "development"
		if info != nil && info.Main.Version != "" {
			version = info.Main.Version
		}
		httpx.JSON(w, 200, map[string]string{"service": "mostlyvers-backend", "version": version})
	})
	r.Get("/privacy", s.privacyPage)
	r.Get("/account-deletion", s.deletionPage)
	r.Route("/v1", func(v chi.Router) {
		v.Post("/auth/signup", s.readerSignup)
		v.Post("/auth/email-otp/request", s.requestSignupOTP)
		v.Post("/auth/login", s.readerLogin)
		v.Post("/auth/refresh", s.readerRefresh)
		v.Post("/auth/logout", s.readerLogout)
		v.Post("/auth/password-reset/request", s.passwordResetRequest)
		v.Post("/auth/password-reset/confirm", s.passwordResetConfirm)
		v.Get("/account-deletion/requests/{token}", s.deletionTokenInfo)
		v.Post("/account-deletion/requests/{token}/confirm", s.confirmDeletion)
		v.Post("/account-deletion/request", s.externalDeletionRequest)
		v.Route("/admin/auth", func(a chi.Router) {
			a.Post("/login", s.adminLogin)
			a.Post("/refresh", s.adminRefresh)
			a.Post("/logout", s.adminLogout)
			a.Post("/password-reset/request", s.passwordResetRequest)
			a.Post("/password-reset/confirm", s.passwordResetConfirm)
		})
		v.Group(func(p chi.Router) { p.Use(s.require("READER")); s.readerRoutes(p) })
		v.Route("/admin", func(a chi.Router) { a.Use(s.require("OWNER"), s.csrf); s.adminRoutes(a) })
		v.Post("/internal/jobs/run", s.runJobs)
	})
	return r
}

func (s *Server) requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-Id")
		if _, err := uuid.Parse(id); err != nil {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), httpx.RequestIDKey, id)))
	})
}
func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if value := recover(); value != nil {
				slog.Error("request panic", "requestId", r.Context().Value(httpx.RequestIDKey), "panic", value)
				httpx.WriteError(w, r, httpx.NewError(500, "INTERNAL_ERROR", "The request could not be completed."))
			}
		}()
		next.ServeHTTP(w, r)
	})
}
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}
func (s *Server) timeout(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		duration := 2 * time.Minute
		if r.URL.Path == "/v1/internal/jobs/run" {
			duration = 12 * time.Minute
		}
		ctx, cancel := context.WithTimeout(r.Context(), duration)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
func (s *Server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && origin == s.cfg.AdminOrigin {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-CSRF-Token, Idempotency-Key, X-Request-Id")
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) require(role string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			principal, err := s.tokens.Verify(httpx.Bearer(r))
			if err != nil || principal.Role != role || !s.store.SessionActive(r.Context(), principal.SessionID) {
				httpx.WriteError(w, r, httpx.NewError(401, "AUTH_EXPIRED", "Authentication is required."))
				return
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), httpx.PrincipalKey, principal)))
		})
	}
}
func (s *Server) csrf(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie("mv_csrf")
		if err != nil || cookie.Value == "" || r.Header.Get("X-CSRF-Token") != cookie.Value {
			httpx.WriteError(w, r, httpx.NewError(403, "CSRF_INVALID", "The security token is invalid."))
			return
		}
		next.ServeHTTP(w, r)
	})
}
func principal(r *http.Request) domain.Principal {
	value, _ := r.Context().Value(httpx.PrincipalKey).(domain.Principal)
	return value
}
func requestID(r *http.Request) string {
	value, _ := r.Context().Value(httpx.RequestIDKey).(string)
	return value
}
func ptr[T any](value T) *T        { return &value }
func cleanURL(value string) string { return strings.TrimRight(value, "/") }
