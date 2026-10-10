package api

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/mostlyvers/backend/internal/content"
	emailpkg "github.com/mostlyvers/backend/internal/email"
	"github.com/mostlyvers/backend/internal/httpx"
	"github.com/mostlyvers/backend/internal/store"
)

type claimedJob struct {
	ID, Kind              string
	Payload               []byte
	Attempts, MaxAttempts int
}

// StartWorker runs the durable PostgreSQL queue inside the monolith. The
// scheduled HTTP wake-up remains a fallback for Render's free-tier sleep.
func (s *Server) StartWorker(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		s.runPendingJobs(ctx, 3)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) runPendingJobs(ctx context.Context, limit int) (processed, failed int) {
	_ = s.maintenance(ctx)
	for range limit {
		job, err := s.claimJob(ctx)
		if errors.Is(err, pgx.ErrNoRows) || ctx.Err() != nil {
			break
		}
		if err != nil {
			break
		}
		processed++
		if err = s.processJob(ctx, job); err != nil {
			failed++
			s.failJob(ctx, job, err)
		} else {
			_, _ = s.store.Pool.Exec(ctx, `UPDATE jobs SET status='COMPLETED',completed_at=now(),locked_at=NULL,locked_by=NULL,last_error=NULL WHERE id=$1`, job.ID)
		}
	}
	return
}

func (s *Server) runJobs(w http.ResponseWriter, r *http.Request) {
	want := "Bearer " + s.cfg.InternalJobToken
	got := r.Header.Get("Authorization")
	if s.cfg.InternalJobToken == "" || len(got) != len(want) || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		httpx.WriteError(w, r, httpx.NewError(401, "INTERNAL_AUTH_REQUIRED", "Internal authentication is required."))
		return
	}
	processed, failed := s.runPendingJobs(r.Context(), 5)
	httpx.JSON(w, 200, map[string]int{"processed": processed, "failed": failed})
}

func (s *Server) claimJob(ctx context.Context) (claimedJob, error) {
	tx, err := s.store.Pool.Begin(ctx)
	if err != nil {
		return claimedJob{}, err
	}
	defer tx.Rollback(ctx)
	var job claimedJob
	err = tx.QueryRow(ctx, `SELECT id,kind,payload,attempts,max_attempts FROM jobs WHERE status='PENDING' AND run_after<=now() ORDER BY run_after,created_at FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&job.ID, &job.Kind, &job.Payload, &job.Attempts, &job.MaxAttempts)
	if err != nil {
		return claimedJob{}, err
	}
	_, err = tx.Exec(ctx, `UPDATE jobs SET status='RUNNING',attempts=attempts+1,locked_at=now(),locked_by=$2 WHERE id=$1`, job.ID, "monolith")
	if err != nil {
		return claimedJob{}, err
	}
	job.Attempts++
	return job, tx.Commit(ctx)
}
func (s *Server) failJob(ctx context.Context, job claimedJob, cause error) {
	status := "PENDING"
	if job.Attempts >= job.MaxAttempts {
		status = "DEAD"
	}
	delay := time.Duration(1<<min(job.Attempts, 10)) * time.Minute
	_, _ = s.store.Pool.Exec(ctx, `UPDATE jobs SET status=$2,run_after=now()+$3::interval,locked_at=NULL,locked_by=NULL,last_error=$4 WHERE id=$1`, job.ID, status, fmt.Sprintf("%d seconds", int(delay.Seconds())), truncate(cause.Error(), 1000))
}
func truncate(value string, n int) string {
	if len(value) <= n {
		return value
	}
	return value[:n]
}

func (s *Server) processJob(ctx context.Context, job claimedJob) error {
	switch job.Kind {
	case "PROCESS_EPUB":
		return s.processEPUBJob(ctx, job.Payload)
	case "DELETE_ACCOUNT":
		return s.processDeletionJob(ctx, job.ID, job.Payload)
	case "DELETE_BOOK_ASSETS":
		return s.processBookAssetDeletionJob(ctx, job.Payload)
	case "CLEANUP":
		return s.cleanup(ctx)
	default:
		return fmt.Errorf("unsupported job kind %q", job.Kind)
	}
}

type bookAssetDeletionPayload struct {
	OperationID    string   `json:"operationId"`
	ObjectKeys     []string `json:"objectKeys"`
	CoverObjectKey string   `json:"coverObjectKey"`
}

func (s *Server) processBookAssetDeletionJob(ctx context.Context, payload []byte) error {
	var input bookAssetDeletionPayload
	if err := json.Unmarshal(payload, &input); err != nil {
		return err
	}
	_, _ = s.store.Pool.Exec(ctx, `UPDATE operations SET status='RUNNING',progress=10,updated_at=now() WHERE id=$1`, input.OperationID)
	for _, key := range input.ObjectKeys {
		if key == "" {
			continue
		}
		if err := s.objects.Delete(ctx, s.objects.PrivateBucket, key); err != nil {
			_, _ = s.store.Pool.Exec(ctx, `UPDATE operations SET status='FAILED',error_code='ASSET_CLEANUP_FAILED',updated_at=now() WHERE id=$1`, input.OperationID)
			return err
		}
	}
	if input.CoverObjectKey != "" {
		s.deleteImage(ctx, input.CoverObjectKey)
	}
	_, err := s.store.Pool.Exec(ctx, `UPDATE operations SET status='COMPLETED',progress=100,updated_at=now() WHERE id=$1`, input.OperationID)
	return err
}

// Local development processes newly attached content immediately so the
// dashboard can observe PROCESSING -> VALID without waiting for the worker tick.
func (s *Server) kickDevelopmentJob(jobID string) {
	if s.cfg.Environment == "production" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
		defer cancel()
		var job claimedJob
		err := s.store.Pool.QueryRow(ctx, `UPDATE jobs SET status='RUNNING',attempts=attempts+1,locked_at=now(),locked_by='development-monolith' WHERE id=$1 AND status='PENDING' RETURNING id,kind,payload,attempts,max_attempts`, jobID).Scan(&job.ID, &job.Kind, &job.Payload, &job.Attempts, &job.MaxAttempts)
		if err != nil {
			return
		}
		if err = s.processJob(ctx, job); err != nil {
			s.failJob(ctx, job, err)
			return
		}
		_, _ = s.store.Pool.Exec(ctx, `UPDATE jobs SET status='COMPLETED',completed_at=now(),locked_at=NULL,locked_by=NULL,last_error=NULL WHERE id=$1`, job.ID)
	}()
}

type epubJobPayload struct {
	OperationID      string `json:"operationId"`
	BookID           string `json:"bookId"`
	ContentVersionID string `json:"contentVersionId"`
	SourceObjectKey  string `json:"sourceObjectKey"`
}

func (s *Server) processEPUBJob(ctx context.Context, payload []byte) error {
	var input epubJobPayload
	if err := json.Unmarshal(payload, &input); err != nil {
		return err
	}
	_, _ = s.store.Pool.Exec(ctx, `UPDATE operations SET status='RUNNING',progress=5,updated_at=now() WHERE id=$1`, input.OperationID)
	body, length, _, err := s.objects.Get(ctx, s.objects.PrivateBucket, input.SourceObjectKey, nil)
	if err != nil {
		return s.failOperation(ctx, input, err)
	}
	defer body.Close()
	if length > 100<<20 {
		return s.failOperation(ctx, input, errors.New("EPUB_TOO_LARGE"))
	}
	raw := make([]byte, length)
	if _, err = io.ReadFull(body, raw); err != nil {
		return s.failOperation(ctx, input, err)
	}
	processed, err := content.ProcessEPUB(raw)
	if err != nil {
		return s.failOperation(ctx, input, err)
	}
	_, _ = s.store.Pool.Exec(ctx, `UPDATE operations SET progress=45,updated_at=now() WHERE id=$1`, input.OperationID)
	for _, resource := range processed.Resources {
		key := path.Join("books", input.BookID, input.ContentVersionID, "resources", resource.ID)
		if err = s.objects.Put(ctx, s.objects.PrivateBucket, key, resource.MediaType, bytes.NewReader(resource.Data), int64(len(resource.Data))); err != nil {
			return s.failOperation(ctx, input, err)
		}
	}
	sanitizedKey := path.Join("books", input.BookID, input.ContentVersionID, "sanitized.epub")
	if err = s.objects.Put(ctx, s.objects.PrivateBucket, sanitizedKey, "application/epub+zip", bytes.NewReader(processed.Sanitized), int64(len(processed.Sanitized))); err != nil {
		return s.failOperation(ctx, input, err)
	}
	ciphertext, key, nonce, checksum, err := content.EncryptPackage(processed.Sanitized)
	if err != nil {
		return s.failOperation(ctx, input, err)
	}
	wrapped, err := content.WrapKey(s.cfg.ContentKEK, key)
	if err != nil {
		return s.failOperation(ctx, input, err)
	}
	encryptedKey := path.Join("books", input.BookID, input.ContentVersionID, "offline.epub.gcm")
	if err = s.objects.Put(ctx, s.objects.PrivateBucket, encryptedKey, "application/octet-stream", bytes.NewReader(ciphertext), int64(len(ciphertext))); err != nil {
		return s.failOperation(ctx, input, err)
	}
	tx, err := s.store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `DELETE FROM book_resources WHERE content_version_id=$1`, input.ContentVersionID); err != nil {
		return err
	}
	for _, resource := range processed.Resources {
		objectKey := path.Join("books", input.BookID, input.ContentVersionID, "resources", resource.ID)
		_, err = tx.Exec(ctx, `INSERT INTO book_resources(id,content_version_id,resource_id,href,media_type,object_key,spine_position,checksum_sha256,content_length) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, store.NewID(), input.ContentVersionID, resource.ID, resource.Href, resource.MediaType, objectKey, resource.SpinePosition, resource.Checksum, len(resource.Data))
		if err != nil {
			return err
		}
	}
	_, err = tx.Exec(ctx, `UPDATE book_content_versions SET sanitized_object_key=$1,encrypted_object_key=$2,encrypted_checksum_sha256=$3,encrypted_content_length=$4,encrypted_nonce=$5,encrypted_content_key=$6,status='VALID',validated_at=now() WHERE id=$7`, sanitizedKey, encryptedKey, checksum, len(ciphertext), nonce, wrapped, input.ContentVersionID)
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE books SET current_content_version_id=$1,version=version+1,updated_at=now() WHERE id=$2`, input.ContentVersionID, input.BookID)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE uploads SET status='VALID' WHERE id=(SELECT source_upload_id FROM book_content_versions WHERE id=$1)`, input.ContentVersionID)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE operations SET status='COMPLETED',progress=100,result=$2,updated_at=now() WHERE id=$1`, input.OperationID, mapJSON(map[string]any{"bookId": input.BookID, "contentVersionId": input.ContentVersionID, "resourceCount": len(processed.Resources)}))
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}
func (s *Server) failOperation(ctx context.Context, input epubJobPayload, cause error) error {
	code := safeContentErrorCode(cause)
	_, _ = s.store.Pool.Exec(ctx, `UPDATE book_content_versions SET status='FAILED',failure_code=$2 WHERE id=$1`, input.ContentVersionID, code)
	_, _ = s.store.Pool.Exec(ctx, `UPDATE uploads SET status='FAILED',error_code=$2 WHERE id=(SELECT source_upload_id FROM book_content_versions WHERE id=$1)`, input.ContentVersionID, code)
	_, _ = s.store.Pool.Exec(ctx, `UPDATE operations SET status='FAILED',error_code=$2,updated_at=now() WHERE id=$1`, input.OperationID, code)
	return cause
}

func safeContentErrorCode(cause error) string {
	code := strings.TrimSpace(strings.SplitN(cause.Error(), ":", 2)[0])
	if !strings.HasPrefix(code, "EPUB_") {
		return "EPUB_PROCESSING_FAILED"
	}
	for _, char := range code {
		if (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' {
			return "EPUB_PROCESSING_FAILED"
		}
	}
	return truncate(code, 100)
}

type deletionJobPayload struct {
	RequestID string `json:"requestId"`
	AccountID string `json:"accountId"`
	Email     string `json:"email"`
}

func (s *Server) processDeletionJob(ctx context.Context, jobID string, payload []byte) error {
	var in deletionJobPayload
	if err := json.Unmarshal(payload, &in); err != nil {
		return err
	}
	var profileKey *string
	_ = s.store.Pool.QueryRow(ctx, `SELECT profile_object_key FROM reader_profiles WHERE account_id=$1`, in.AccountID).Scan(&profileKey)
	tx, err := s.store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	statements := []string{`DELETE FROM device_changes WHERE reader_id=$1`, `DELETE FROM prebooks WHERE reader_id=$1`, `DELETE FROM entitlements WHERE reader_id=$1`, `DELETE FROM feedback WHERE reader_id=$1`, `DELETE FROM reading_progress WHERE reader_id=$1`, `DELETE FROM bookmarks WHERE reader_id=$1`, `DELETE FROM downloads WHERE reader_id=$1`, `DELETE FROM offline_licenses WHERE reader_id=$1`, `DELETE FROM reading_sessions WHERE reader_id=$1`, `DELETE FROM integrity_verifications WHERE reader_id=$1`, `DELETE FROM devices WHERE reader_id=$1`, `DELETE FROM auth_sessions WHERE account_id=$1`, `DELETE FROM one_time_tokens WHERE account_id=$1`, `UPDATE transactions SET checkout_id=NULL WHERE reader_id=$1`, `DELETE FROM checkouts WHERE reader_id=$1`, `UPDATE transactions SET reader_id=NULL,provider_reference=NULL WHERE reader_id=$1`, `UPDATE audit_events SET actor_id=NULL WHERE actor_id=$1`, `UPDATE audit_events SET target_id=NULL WHERE target_type='ACCOUNT' AND target_id=$1`}
	for _, statement := range statements {
		if _, err = tx.Exec(ctx, statement, in.AccountID); err != nil {
			return err
		}
	}
	pseudonym := "deleted+" + in.AccountID + "@invalid.local"
	_, err = tx.Exec(ctx, `UPDATE reader_profiles SET name='Deleted Reader',age=13,gender='Deleted',phone='',profile_object_key=NULL,deleted_at=now() WHERE account_id=$1`, in.AccountID)
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE accounts SET email=$2,normalized_email=$2,password_hash='DELETED',status='DELETED',updated_at=now() WHERE id=$1`, in.AccountID, pseudonym)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE account_deletion_requests SET account_id=NULL,normalized_email='deleted:'||id::text,status=CASE WHEN id=$2 THEN 'COMPLETED' ELSE 'EXPIRED' END,completed_at=CASE WHEN id=$2 THEN now() ELSE completed_at END WHERE account_id=$1`, in.AccountID, in.RequestID)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE jobs SET payload=$2 WHERE id=$1`, jobID, mapJSON(map[string]any{"requestId": in.RequestID, "accountId": "deleted"}))
	}
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if profileKey != nil {
		s.deleteImage(ctx, *profileKey)
	}
	_ = s.email.Send(ctx, emailpkg.Message{To: in.Email, Subject: "Your MOSTLYVERS account has been deleted", HTML: "<p>Your MOSTLYVERS reader account and personal data have been deleted. Legally required records have been pseudonymized.</p>"})
	return nil
}

func (s *Server) cleanup(ctx context.Context) error {
	_, err := s.store.Pool.Exec(ctx, `DELETE FROM auth_sessions WHERE expires_at<now()-interval '7 days'; DELETE FROM one_time_tokens WHERE expires_at<now()-interval '7 days'; DELETE FROM reading_sessions WHERE expires_at<now()-interval '1 day'; UPDATE account_deletion_requests SET status='EXPIRED' WHERE status='PENDING' AND expires_at<now()`)
	return err
}

func (s *Server) maintenance(ctx context.Context) error {
	_ = s.cleanup(ctx)
	_, _ = s.store.Pool.Exec(ctx, `UPDATE jobs SET status='PENDING',locked_at=NULL,locked_by=NULL,last_error=COALESCE(last_error,'worker interrupted') WHERE status='RUNNING' AND locked_at<now()-interval '15 minutes'`)
	rows, err := s.store.Pool.Query(ctx, `SELECT id,bucket,object_key,provider_upload_id FROM uploads WHERE status IN ('CREATED','UPLOADING') AND expires_at<now() LIMIT 50`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var id, bucket, key, provider string
			if rows.Scan(&id, &bucket, &key, &provider) == nil {
				_ = s.objects.AbortMultipart(ctx, bucket, key, provider)
				_, _ = s.store.Pool.Exec(ctx, `UPDATE uploads SET status='ABORTED',error_code='UPLOAD_EXPIRED' WHERE id=$1`, id)
			}
		}
	}
	var due bool
	if s.store.Pool.QueryRow(ctx, `SELECT reconciled_at IS NULL OR reconciled_at<now()-interval '1 hour' FROM storage_usage WHERE id=true`).Scan(&due) == nil && due {
		if usage, usageErr := s.objects.Usage(ctx); usageErr == nil {
			_, _ = s.store.Pool.Exec(ctx, `UPDATE storage_usage SET tracked_bytes=$1,reconciled_at=now() WHERE id=true`, usage)
		}
	}
	return err
}
