package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"time"

	"github.com/mostlyvers/backend/internal/domain"
	"github.com/mostlyvers/backend/internal/integrity"
	"github.com/mostlyvers/backend/internal/store"
)

func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
func (s *Server) allowRate(ctx context.Context, key string, maximum int, window time.Duration) bool {
	sum := sha256.Sum256([]byte(key))
	bucket := hex.EncodeToString(sum[:])
	var attempts int
	err := s.store.Pool.QueryRow(ctx, `INSERT INTO rate_limits(bucket_key,attempts,window_started_at) VALUES($1,1,now()) ON CONFLICT(bucket_key) DO UPDATE SET attempts=CASE WHEN rate_limits.window_started_at<now()-$2::interval THEN 1 ELSE rate_limits.attempts+1 END,window_started_at=CASE WHEN rate_limits.window_started_at<now()-$2::interval THEN now() ELSE rate_limits.window_started_at END RETURNING attempts`, bucket, window.String()).Scan(&attempts)
	return err == nil && attempts <= maximum
}
func (s *Server) recordIntegrity(r *http.Request, accountID, deviceID string, result integrity.Result) {
	_, _ = s.store.Pool.Exec(r.Context(), `INSERT INTO integrity_verifications(id,reader_id,device_id,request_hash,verdict,expires_at) VALUES($1,$2,$3,$4,$5,$6)`, store.NewID(), accountID, deviceID, result.RequestHash, result.Verdict, result.ExpiresAt)
}
func (s *Server) signReaderPicture(ctx context.Context, profile map[string]any) {
	key, ok := profile["profilePictureUrl"].(*string)
	if !ok || key == nil {
		return
	}
	_ = ctx
	profile["profilePictureUrl"] = s.imageURL(*key)
}
func (s *Server) signOwnerPicture(ctx context.Context, owner *domain.Owner) {
	if owner.ProfilePictureURL == nil {
		return
	}
	_ = ctx
	value := s.imageURL(*owner.ProfilePictureURL)
	owner.ProfilePictureURL = &value
}
