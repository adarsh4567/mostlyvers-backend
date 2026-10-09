package api

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/mostlyvers/backend/internal/content"
	"github.com/mostlyvers/backend/internal/domain"
	"github.com/mostlyvers/backend/internal/httpx"
	"github.com/mostlyvers/backend/internal/store"
)

func (s *Server) myBooks(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	items, err := s.queryBooks(r.Context(), p.AccountID, `EXISTS(SELECT 1 FROM entitlements e WHERE e.reader_id=$1 AND e.book_id=b.id AND e.status='ACTIVE')`)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	for _, item := range items {
		bookID := item["id"].(string)
		var purchased time.Time
		_ = s.store.Pool.QueryRow(r.Context(), `SELECT granted_at FROM entitlements WHERE reader_id=$1 AND book_id=$2 AND status='ACTIVE'`, p.AccountID, bookID).Scan(&purchased)
		item["purchasedAt"] = purchased
		if progress, err := s.progress(r.Context(), p.AccountID, bookID); err == nil {
			item["progress"] = progress
		}
		if bookmark, err := s.bookmark(r.Context(), p.AccountID, bookID); err == nil {
			item["bookmark"] = bookmark
		}
	}
	httpx.JSON(w, 200, domain.CursorPage[map[string]any]{Items: items})
}
func (s *Server) progress(ctx context.Context, readerID, bookID string) (domain.ReadingProgress, error) {
	var p domain.ReadingProgress
	p.BookID = bookID
	err := s.store.Pool.QueryRow(ctx, `SELECT href,cfi,progression,displayed_page,displayed_page_count,progress_percent,updated_at,version FROM reading_progress WHERE reader_id=$1 AND book_id=$2`, readerID, bookID).Scan(&p.Locator.Href, &p.Locator.CFI, &p.Locator.Progression, &p.Locator.DisplayedPage, &p.Locator.DisplayedPageCount, &p.ProgressPercent, &p.UpdatedAt, &p.Version)
	return p, err
}
func (s *Server) bookmark(ctx context.Context, readerID, bookID string) (domain.ReadingLocator, error) {
	var b domain.ReadingLocator
	err := s.store.Pool.QueryRow(ctx, `SELECT href,cfi,progression,displayed_page,displayed_page_count FROM bookmarks WHERE reader_id=$1 AND book_id=$2`, readerID, bookID).Scan(&b.Href, &b.CFI, &b.Progression, &b.DisplayedPage, &b.DisplayedPageCount)
	return b, err
}
func (s *Server) createReadingSession(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	bookID := chi.URLParam(r, "bookID")
	deviceID, err := s.requireFull(r.Context(), p)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err = s.requireOwned(r.Context(), p.AccountID, bookID); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var contentID, title string
	err = s.store.Pool.QueryRow(r.Context(), `SELECT b.current_content_version_id,b.title FROM books b JOIN book_content_versions c ON c.id=b.current_content_version_id AND c.status='VALID' WHERE b.id=$1 AND b.unavailable_at IS NULL`, bookID).Scan(&contentID, &title)
	if err != nil {
		httpx.WriteError(w, r, httpx.NewError(409, "CONTENT_UNAVAILABLE", "Book content is unavailable."))
		return
	}
	sessionID := store.NewID()
	expires := time.Now().Add(15 * time.Minute)
	_, err = s.store.Pool.Exec(r.Context(), `INSERT INTO reading_sessions(id,reader_id,book_id,device_id,content_version_id,expires_at) VALUES($1,$2,$3,$4,$5,$6)`, sessionID, p.AccountID, bookID, deviceID, contentID, expires)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	rows, err := s.store.Pool.Query(r.Context(), `SELECT resource_id,href,media_type,spine_position FROM book_resources WHERE content_version_id=$1 ORDER BY spine_position NULLS LAST,resource_id`, contentID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	resources := []map[string]any{}
	spine := []string{}
	for rows.Next() {
		var id, href, media string
		var pos *int
		_ = rows.Scan(&id, &href, &media, &pos)
		resources = append(resources, map[string]any{"id": id, "href": href, "mediaType": media})
		if pos != nil {
			spine = append(spine, id)
		}
	}
	result := map[string]any{"id": sessionID, "expiresAt": expires, "bookId": bookID, "title": title, "resources": resources, "spine": spine}
	if pgr, err := s.progress(r.Context(), p.AccountID, bookID); err == nil {
		result["progress"] = pgr
	}
	if mark, err := s.bookmark(r.Context(), p.AccountID, bookID); err == nil {
		result["bookmark"] = mark
	}
	httpx.JSON(w, 201, result)
}
func (s *Server) readResource(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	sessionID, resourceID := chi.URLParam(r, "sessionID"), chi.URLParam(r, "resourceID")
	var objectKey, media, deviceID, bookID string
	err := s.store.Pool.QueryRow(r.Context(), `SELECT br.object_key,br.media_type,rs.device_id,rs.book_id FROM reading_sessions rs JOIN book_resources br ON br.content_version_id=rs.content_version_id AND br.resource_id=$2 WHERE rs.id=$1 AND rs.reader_id=$3 AND rs.expires_at>now()`, sessionID, resourceID, p.AccountID).Scan(&objectKey, &media, &deviceID, &bookID)
	if err != nil {
		httpx.WriteError(w, r, httpx.NewError(403, "READING_SESSION_EXPIRED", "The reading session has expired."))
		return
	}
	current, err := s.requireFull(r.Context(), p)
	if err != nil || current != deviceID {
		httpx.WriteError(w, r, httpx.NewError(403, "DEVICE_TRANSFER_REQUIRED", "This device is not authorized."))
		return
	}
	if err = s.requireOwned(r.Context(), p.AccountID, bookID); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	body, length, _, err := s.objects.Get(r.Context(), s.objects.PrivateBucket, objectKey, nil)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer body.Close()
	w.Header().Set("Content-Type", media)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	_, _ = io.Copy(w, body)
}
func (s *Server) getProgress(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	bookID := chi.URLParam(r, "bookID")
	if err := s.requireOwned(r.Context(), p.AccountID, bookID); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	progress, err := s.progress(r.Context(), p.AccountID, bookID)
	if err == pgx.ErrNoRows {
		w.WriteHeader(204)
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, 200, progress)
}

type progressInput struct {
	Locator         domain.ReadingLocator `json:"locator"`
	ProgressPercent float64               `json:"progressPercent"`
	Version         int                   `json:"version"`
}

func validateLocator(l domain.ReadingLocator) bool {
	return l.Href != "" && l.Progression >= 0 && l.Progression <= 1
}
func (s *Server) putProgress(w http.ResponseWriter, r *http.Request) {
	var input progressInput
	if !httpx.Decode(w, r, &input) {
		return
	}
	if !validateLocator(input.Locator) || input.ProgressPercent < 0 || input.ProgressPercent > 100 {
		httpx.WriteError(w, r, httpx.NewError(422, "VALIDATION_ERROR", "Reading locator is invalid."))
		return
	}
	p := principal(r)
	bookID := chi.URLParam(r, "bookID")
	if _, err := s.requireFull(r.Context(), p); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := s.requireOwned(r.Context(), p.AccountID, bookID); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var saved domain.ReadingProgress
	saved.BookID = bookID
	err := s.store.Pool.QueryRow(r.Context(), `INSERT INTO reading_progress(reader_id,book_id,href,cfi,progression,displayed_page,displayed_page_count,progress_percent,version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,1) ON CONFLICT(reader_id,book_id) DO UPDATE SET href=excluded.href,cfi=excluded.cfi,progression=excluded.progression,displayed_page=excluded.displayed_page,displayed_page_count=excluded.displayed_page_count,progress_percent=excluded.progress_percent,version=reading_progress.version+1,updated_at=now() WHERE reading_progress.version=$9 RETURNING href,cfi,progression,displayed_page,displayed_page_count,progress_percent,updated_at,version`, p.AccountID, bookID, input.Locator.Href, input.Locator.CFI, input.Locator.Progression, input.Locator.DisplayedPage, input.Locator.DisplayedPageCount, input.ProgressPercent, input.Version).Scan(&saved.Locator.Href, &saved.Locator.CFI, &saved.Locator.Progression, &saved.Locator.DisplayedPage, &saved.Locator.DisplayedPageCount, &saved.ProgressPercent, &saved.UpdatedAt, &saved.Version)
	if err == pgx.ErrNoRows {
		current, _ := s.progress(r.Context(), p.AccountID, bookID)
		apiErr := httpx.NewError(409, "CONTENT_VERSION_CHANGED", "Newer reading progress exists on the server.")
		apiErr.Details = map[string]any{"current": current}
		httpx.WriteError(w, r, apiErr)
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, 200, saved)
}
func (s *Server) putBookmark(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Locator domain.ReadingLocator `json:"locator"`
	}
	if !httpx.Decode(w, r, &input) {
		return
	}
	if !validateLocator(input.Locator) {
		httpx.WriteError(w, r, httpx.NewError(422, "VALIDATION_ERROR", "Reading locator is invalid."))
		return
	}
	p := principal(r)
	bookID := chi.URLParam(r, "bookID")
	if _, err := s.requireFull(r.Context(), p); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := s.requireOwned(r.Context(), p.AccountID, bookID); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_, err := s.store.Pool.Exec(r.Context(), `INSERT INTO bookmarks(reader_id,book_id,href,cfi,progression,displayed_page,displayed_page_count) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(reader_id,book_id) DO UPDATE SET href=excluded.href,cfi=excluded.cfi,progression=excluded.progression,displayed_page=excluded.displayed_page,displayed_page_count=excluded.displayed_page_count,updated_at=now()`, p.AccountID, bookID, input.Locator.Href, input.Locator.CFI, input.Locator.Progression, input.Locator.DisplayedPage, input.Locator.DisplayedPageCount)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, 200, map[string]any{"bookId": bookID, "locator": input.Locator})
}
func (s *Server) deleteBookmark(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	_, err := s.store.Pool.Exec(r.Context(), `DELETE FROM bookmarks WHERE reader_id=$1 AND book_id=$2`, p.AccountID, chi.URLParam(r, "bookID"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(204)
}

type packageData struct {
	ContentID, ObjectKey, Checksum string
	Version                        int
	Length                         int64
	Nonce, WrappedKey              []byte
}

func (s *Server) packageForBook(ctx context.Context, bookID string) (packageData, error) {
	var p packageData
	err := s.store.Pool.QueryRow(ctx, `SELECT c.id,c.version,c.encrypted_object_key,c.encrypted_checksum_sha256,c.encrypted_content_length,c.encrypted_nonce,c.encrypted_content_key FROM books b JOIN book_content_versions c ON c.id=b.current_content_version_id AND c.status='VALID' WHERE b.id=$1 AND b.unavailable_at IS NULL`, bookID).Scan(&p.ContentID, &p.Version, &p.ObjectKey, &p.Checksum, &p.Length, &p.Nonce, &p.WrappedKey)
	return p, err
}
func (s *Server) offlineLicense(w http.ResponseWriter, r *http.Request) {
	s.issueOfflineLicense(w, r, false)
}
func (s *Server) renewOfflineLicense(w http.ResponseWriter, r *http.Request) {
	s.issueOfflineLicense(w, r, true)
}
func (s *Server) issueOfflineLicense(w http.ResponseWriter, r *http.Request, renew bool) {
	p := principal(r)
	bookID := chi.URLParam(r, "bookID")
	deviceID, err := s.requireFull(r.Context(), p)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err = s.requireOwned(r.Context(), p.AccountID, bookID); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	pkg, err := s.packageForBook(r.Context(), bookID)
	if err != nil {
		httpx.WriteError(w, r, httpx.NewError(409, "CONTENT_UNAVAILABLE", "Offline content is unavailable."))
		return
	}
	if renew {
		var priorContent string
		err = s.store.Pool.QueryRow(r.Context(), `SELECT content_version_id FROM downloads WHERE reader_id=$1 AND book_id=$2 AND removed_at IS NULL`, p.AccountID, bookID).Scan(&priorContent)
		if err != nil {
			httpx.WriteError(w, r, httpx.NewError(404, "DOWNLOAD_NOT_FOUND", "Download not found."))
			return
		}
		if priorContent != pkg.ContentID {
			httpx.WriteError(w, r, httpx.NewError(409, "CONTENT_VERSION_CHANGED", "A newer book package must be downloaded."))
			return
		}
	}
	key, err := content.UnwrapKey(s.cfg.ContentKEK, pkg.WrappedKey)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	licenseID := store.NewID()
	expiry := time.Now().Add(30 * 24 * time.Hour)
	_, err = s.store.Pool.Exec(r.Context(), `INSERT INTO offline_licenses(id,reader_id,book_id,device_id,content_version_id,expires_at) VALUES($1,$2,$3,$4,$5,$6)`, licenseID, p.AccountID, bookID, deviceID, pkg.ContentID, expiry)
	if err == nil {
		_, err = s.store.Pool.Exec(r.Context(), `INSERT INTO downloads(reader_id,book_id,content_version_id,license_id) VALUES($1,$2,$3,$4) ON CONFLICT(reader_id,book_id) DO UPDATE SET content_version_id=excluded.content_version_id,license_id=excluded.license_id,downloaded_at=now(),removed_at=NULL`, p.AccountID, bookID, pkg.ContentID, licenseID)
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, 200, map[string]any{"bookId": bookID, "packageVersion": strconv.Itoa(pkg.Version), "packagePath": "/me/books/" + bookID + "/offline-package/" + strconv.Itoa(pkg.Version), "checksumSha256": pkg.Checksum, "contentLength": pkg.Length, "contentKey": content.Base64(key), "nonce": content.Base64(pkg.Nonce), "leaseExpiresAt": expiry})
}
func (s *Server) offlinePackage(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	bookID := chi.URLParam(r, "bookID")
	deviceID, err := s.requireFull(r.Context(), p)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	version, err := strconv.Atoi(chi.URLParam(r, "version"))
	if err != nil {
		httpx.WriteError(w, r, httpx.NewError(404, "PACKAGE_NOT_FOUND", "Package not found."))
		return
	}
	var key string
	err = s.store.Pool.QueryRow(r.Context(), `SELECT c.encrypted_object_key FROM offline_licenses l JOIN book_content_versions c ON c.id=l.content_version_id WHERE l.reader_id=$1 AND l.book_id=$2 AND l.device_id=$3 AND l.expires_at>now() AND l.revoked_at IS NULL AND c.version=$4 ORDER BY l.issued_at DESC LIMIT 1`, p.AccountID, bookID, deviceID, version).Scan(&key)
	if err != nil {
		httpx.WriteError(w, r, httpx.NewError(403, "OFFLINE_LICENSE_EXPIRED", "The offline license is missing or expired."))
		return
	}
	signed, err := s.objects.PresignGet(r.Context(), s.objects.PrivateBucket, key)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	http.Redirect(w, r, signed, http.StatusTemporaryRedirect)
}
func (s *Server) downloads(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	rows, err := s.store.Pool.Query(r.Context(), `SELECT d.book_id,c.version,d.downloaded_at,l.expires_at,c.encrypted_content_length FROM downloads d JOIN offline_licenses l ON l.id=d.license_id JOIN book_content_versions c ON c.id=d.content_version_id WHERE d.reader_id=$1 AND d.removed_at IS NULL ORDER BY d.downloaded_at DESC`, p.AccountID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var bookID string
		var version int
		var downloaded, expires time.Time
		var length int64
		_ = rows.Scan(&bookID, &version, &downloaded, &expires, &length)
		books, _ := s.queryBooks(r.Context(), p.AccountID, `b.id=$2`, bookID)
		if len(books) > 0 {
			items = append(items, map[string]any{"book": books[0], "packageVersion": strconv.Itoa(version), "downloadedAt": downloaded, "leaseExpiresAt": expires, "contentLength": length})
		}
	}
	httpx.JSON(w, 200, domain.CursorPage[map[string]any]{Items: items})
}
func (s *Server) deleteDownload(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	bookID := chi.URLParam(r, "bookID")
	_, err := s.store.Pool.Exec(r.Context(), `UPDATE downloads SET removed_at=now() WHERE reader_id=$1 AND book_id=$2`, p.AccountID, bookID)
	if err == nil {
		_, err = s.store.Pool.Exec(r.Context(), `UPDATE offline_licenses SET revoked_at=now() WHERE reader_id=$1 AND book_id=$2 AND revoked_at IS NULL`, p.AccountID, bookID)
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(204)
}
