package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/mostlyvers/backend/internal/domain"
	"github.com/mostlyvers/backend/internal/httpx"
	"github.com/mostlyvers/backend/internal/store"
)

func (s *Server) adminRoutes(r chi.Router) {
	r.Get("/me", s.adminMe)
	r.Patch("/me", s.updateAdminMe)
	r.Get("/dashboard", s.adminDashboard)
	r.Get("/books", s.adminBooks)
	r.Post("/books", s.createBook)
	r.Get("/books/{bookID}", s.adminBook)
	r.Patch("/books/{bookID}", s.updateBook)
	r.Delete("/books/{bookID}", s.deleteBook)
	r.Post("/books/{bookID}/publish", s.publishBook)
	r.Post("/books/{bookID}/archive", s.archiveBook)
	r.Get("/books/{bookID}/sales", s.bookSales)
	r.Post("/uploads", s.createUpload)
	r.Post("/media", s.adminMediaUpload)
	r.Post("/uploads/{uploadID}/parts", s.uploadParts)
	r.Post("/uploads/{uploadID}/complete", s.completeUpload)
	r.Delete("/uploads/{uploadID}", s.abortUpload)
	r.Get("/operations/{operationID}", s.operation)
	r.Get("/readers", s.adminReaders)
	r.Get("/readers/{readerID}", s.adminReader)
	r.Get("/transactions", s.adminTransactions)
	r.Get("/transactions/{transactionID}", s.adminTransaction)
	r.Get("/sales/summary", s.salesSummary)
	r.Get("/sales/timeseries", s.salesTimeseries)
	r.Get("/sales/export", s.salesExport)
	r.Get("/feedback", s.adminFeedback)
	r.Get("/feedback/{feedbackID}", s.adminFeedbackItem)
	r.Get("/youtube-assets", s.adminYoutubeAssets)
	r.Put("/books/{bookID}/youtube-asset", s.putYoutubeAsset)
	r.Delete("/books/{bookID}/youtube-asset", s.removeYoutubeAsset)
	r.Get("/content/author", s.adminAuthor)
	r.Patch("/content/author", s.updateAdminAuthor)
	r.Get("/content/contact", s.adminContact)
	r.Patch("/content/contact", s.updateAdminContact)
	r.Get("/content/about-app", s.adminAboutApp)
	r.Patch("/content/about-app", s.updateAdminAboutApp)
	r.Get("/settings/app", s.adminSettings)
	r.Patch("/settings/app", s.updateAdminSettings)
	r.Get("/settings/billing", s.paymentStatus)
	r.Get("/payment-status", s.paymentStatus)
	r.Get("/settlement", s.settlement)
	r.Post("/settlement/portal-session", s.paymentUnavailable)
	r.Get("/notifications", s.adminNotifications)
	r.Post("/notifications", s.sendAdminNotification)
	r.Get("/jobs", s.adminJobs)
}

func (s *Server) adminJobs(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.Pool.Query(r.Context(), `SELECT id,kind,status,attempts,max_attempts,run_after,last_error,created_at,completed_at FROM jobs ORDER BY created_at DESC LIMIT 100`)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, kind, status string
		var attempts, maximum int
		var runAfter, createdAt time.Time
		var lastError *string
		var completedAt *time.Time
		if rows.Scan(&id, &kind, &status, &attempts, &maximum, &runAfter, &lastError, &createdAt, &completedAt) != nil {
			continue
		}
		items = append(items, map[string]any{"id": id, "kind": kind, "status": status, "attempts": attempts, "maxAttempts": maximum, "runAfter": runAfter, "lastError": lastError, "createdAt": createdAt, "completedAt": completedAt})
	}
	httpx.JSON(w, 200, domain.CursorPage[map[string]any]{Items: items})
}

func (s *Server) adminMe(w http.ResponseWriter, r *http.Request) {
	owner, err := s.store.OwnerProfile(r.Context(), principal(r).AccountID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	s.signOwnerPicture(r.Context(), &owner)
	httpx.JSON(w, 200, owner)
}
func (s *Server) updateAdminMe(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name                    string  `json:"name"`
		Email                   string  `json:"email"`
		Phone                   string  `json:"phone"`
		ProfilePictureUploadRef *string `json:"profilePictureUploadRef"`
	}
	if !httpx.Decode(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.Name) == "" || !validateEmail(input.Email) || strings.TrimSpace(input.Phone) == "" {
		httpx.WriteError(w, r, httpx.NewError(422, "VALIDATION_ERROR", "Owner profile fields are invalid."))
		return
	}
	p := principal(r)
	tx, err := s.store.Pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var objectKey *string
	if input.ProfilePictureUploadRef != nil {
		_ = tx.QueryRow(r.Context(), `SELECT object_key FROM uploads WHERE id=$1 AND owner_id=$2 AND kind IN ('OWNER_PHOTO','PROFILE_IMAGE') AND status='VALID'`, *input.ProfilePictureUploadRef, p.AccountID).Scan(&objectKey)
	}
	_, err = tx.Exec(r.Context(), `UPDATE accounts SET email=$1,normalized_email=$2,updated_at=now() WHERE id=$3`, strings.TrimSpace(input.Email), store.NormalizeEmail(input.Email), p.AccountID)
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE owner_profiles SET name=$1,phone=$2,profile_object_key=COALESCE($3,profile_object_key) WHERE account_id=$4`, strings.TrimSpace(input.Name), strings.TrimSpace(input.Phone), objectKey, p.AccountID)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	s.adminMe(w, r)
}

func (s *Server) adminDashboard(w http.ResponseWriter, r *http.Request) {
	var total, published, upcoming, readers, purchases, changes, feedback int
	var revenue int64
	_ = s.store.Pool.QueryRow(r.Context(), `SELECT (SELECT count(*) FROM books),(SELECT count(*) FROM books WHERE status='PUBLISHED'),(SELECT count(*) FROM books WHERE status='UPCOMING'),(SELECT count(*) FROM accounts WHERE role='READER' AND status='ACTIVE'),(SELECT count(*) FROM transactions WHERE type='BOOK_PURCHASE' AND status='COMPLETED'),COALESCE((SELECT sum(amount_minor) FROM transactions WHERE status='COMPLETED'),0),(SELECT count(*) FROM device_changes),(SELECT count(*) FROM feedback WHERE created_at>now()-interval '30 days')`).Scan(&total, &published, &upcoming, &readers, &purchases, &revenue, &changes, &feedback)
	transactions := s.transactionItems(r, 5)
	feedbackItems := s.feedbackItems(r, 5)
	top := s.topBooks(r, 5)
	series := s.revenueSeries(r)
	httpx.JSON(w, 200, map[string]any{"metrics": map[string]any{"totalBooks": total, "publishedBooks": published, "upcomingBooks": upcoming, "totalReaders": readers, "totalPurchases": purchases, "totalRevenue": domain.Money{AmountMinor: revenue, Currency: "INR"}, "deviceChanges": changes, "newFeedback": feedback}, "salesSeries": series, "topBooks": top, "recentTransactions": transactions, "recentFeedback": feedbackItems})
}
func (s *Server) revenueSeries(r *http.Request) []map[string]any {
	rows, err := s.store.Pool.Query(r.Context(), `SELECT to_char(day,'DD Mon'),COALESCE(sum(t.amount_minor),0) FROM generate_series(current_date-27,current_date,interval '4 days') day LEFT JOIN transactions t ON t.created_at::date BETWEEN day::date AND (day+interval '3 days')::date AND t.status='COMPLETED' GROUP BY day ORDER BY day`)
	if err != nil {
		return []map[string]any{}
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var label string
		var value int64
		_ = rows.Scan(&label, &value)
		out = append(out, map[string]any{"label": label, "revenueMinor": value})
	}
	return out
}
func (s *Server) topBooks(r *http.Request, limit int) []map[string]any {
	rows, err := s.store.Pool.Query(r.Context(), `SELECT b.id,b.title,b.cover_object_key,count(t.id),COALESCE(sum(t.amount_minor),0),COALESCE(max(t.currency),'INR') FROM books b LEFT JOIN transactions t ON t.book_id=b.id AND t.type='BOOK_PURCHASE' AND t.status='COMPLETED' GROUP BY b.id ORDER BY count(t.id) DESC,b.title LIMIT $1`, limit)
	if err != nil {
		return []map[string]any{}
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, title, currency string
		var cover *string
		var count int
		var revenue int64
		_ = rows.Scan(&id, &title, &cover, &count, &revenue, &currency)
		coverURL := ""
		if cover != nil {
			coverURL = s.imageURL(*cover)
		}
		out = append(out, map[string]any{"id": id, "title": title, "coverUrl": coverURL, "purchases": count, "revenue": domain.Money{AmountMinor: revenue, Currency: currency}})
	}
	return out
}

type adminBookInput struct {
	Version          *int         `json:"version"`
	Title            string       `json:"title"`
	ShortDescription string       `json:"shortDescription"`
	PublicationMonth int          `json:"publicationMonth"`
	PublicationYear  int          `json:"publicationYear"`
	Price            domain.Money `json:"price"`
	Status           string       `json:"status"`
	CoverUploadRef   *string      `json:"coverUploadRef"`
	ContentUploadRef *string      `json:"contentUploadRef"`
	Prebook          struct {
		Enabled         bool `json:"enabled"`
		DiscountPercent int  `json:"discountPercent"`
	} `json:"prebook"`
	YoutubeAsset *struct {
		SongName   string `json:"songName"`
		YoutubeURL string `json:"youtubeUrl"`
	} `json:"youtubeAsset"`
}

func validateBookInput(input adminBookInput) error {
	if strings.TrimSpace(input.Title) == "" || strings.TrimSpace(input.ShortDescription) == "" || input.PublicationMonth < 1 || input.PublicationMonth > 12 || input.PublicationYear < 2000 || input.Price.AmountMinor < 0 {
		return httpx.NewError(422, "VALIDATION_ERROR", "Book fields are invalid.")
	}
	if input.Status != "DRAFT" && input.Status != "UPCOMING" && input.Status != "PUBLISHED" && input.Status != "ARCHIVED" {
		return httpx.NewError(422, "VALIDATION_ERROR", "Book status is invalid.")
	}
	if input.Prebook.DiscountPercent < 0 || input.Prebook.DiscountPercent > 90 {
		return httpx.NewError(422, "VALIDATION_ERROR", "Pre-book discount is invalid.")
	}
	if input.YoutubeAsset != nil && !validYouTube(input.YoutubeAsset.YoutubeURL) {
		return httpx.NewError(422, "INVALID_YOUTUBE_URL", "Enter an HTTPS YouTube URL.")
	}
	return nil
}
func (s *Server) createBook(w http.ResponseWriter, r *http.Request) {
	var input adminBookInput
	if !httpx.Decode(w, r, &input) {
		return
	}
	if err := validateBookInput(input); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if input.Status == "PUBLISHED" || input.Status == "ARCHIVED" {
		httpx.WriteError(w, r, httpx.NewError(422, "INVALID_STATUS_TRANSITION", "Use the publish or archive action for this status."))
		return
	}
	p := principal(r)
	id := store.NewID()
	var cover *string
	if input.CoverUploadRef != nil {
		if err := s.store.Pool.QueryRow(r.Context(), `SELECT object_key FROM uploads WHERE id=$1 AND owner_id=$2 AND kind='BOOK_COVER' AND status='VALID'`, *input.CoverUploadRef, p.AccountID).Scan(&cover); err != nil {
			httpx.WriteError(w, r, httpx.NewError(422, "COVER_UPLOAD_INVALID", "The cover upload is not valid."))
			return
		}
	}
	_, err := s.store.Pool.Exec(r.Context(), `INSERT INTO books(id,title,short_description,cover_object_key,publication_month,publication_year,price_minor,currency,status,prebook_enabled,prebook_discount) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, id, strings.TrimSpace(input.Title), strings.TrimSpace(input.ShortDescription), cover, input.PublicationMonth, input.PublicationYear, input.Price.AmountMinor, currency(input.Price.Currency), input.Status, input.Prebook.Enabled, input.Prebook.DiscountPercent)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if input.ContentUploadRef != nil {
		if err = s.attachContentUpload(r, p.AccountID, id, *input.ContentUploadRef); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	if input.YoutubeAsset != nil {
		_, _ = s.store.Pool.Exec(r.Context(), `INSERT INTO youtube_assets(book_id,song_name,youtube_url,updated_by) VALUES($1,$2,$3,$4)`, id, input.YoutubeAsset.SongName, input.YoutubeAsset.YoutubeURL, p.AccountID)
	}
	s.store.Audit(r.Context(), p.AccountID, p.Role, "BOOK_CREATED", "BOOK", &id, requestID(r), nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("bookID", id)
	s.adminBook(w, r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx)))
}
func currency(value string) string {
	if len(value) == 3 {
		return strings.ToUpper(value)
	}
	return "INR"
}
func (s *Server) attachContentUpload(r *http.Request, ownerID, bookID, uploadID string) error {
	var key string
	var status string
	err := s.store.Pool.QueryRow(r.Context(), `SELECT object_key,status FROM uploads WHERE id=$1 AND owner_id=$2 AND kind='EPUB'`, uploadID, ownerID).Scan(&key, &status)
	if err != nil {
		return err
	}
	if status != "UPLOADED" {
		return httpx.NewError(409, "UPLOAD_NOT_READY", "The EPUB upload is not ready for processing.")
	}
	var version int
	_ = s.store.Pool.QueryRow(r.Context(), `SELECT COALESCE(max(version),0)+1 FROM book_content_versions WHERE book_id=$1`, bookID).Scan(&version)
	contentID, operationID, jobID := store.NewID(), store.NewID(), store.NewID()
	tx, err := s.store.Pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), `INSERT INTO book_content_versions(id,book_id,version,source_upload_id,source_object_key) VALUES($1,$2,$3,$4,$5)`, contentID, bookID, version, uploadID, key)
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO operations(id,kind,created_by,result) VALUES($1,'PROCESS_EPUB',$2,$3)`, operationID, ownerID, mapJSON(map[string]any{"bookId": bookID, "contentVersionId": contentID}))
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO jobs(id,kind,payload) VALUES($1,'PROCESS_EPUB',$2)`, jobID, mapJSON(map[string]any{"operationId": operationID, "bookId": bookID, "contentVersionId": contentID, "sourceObjectKey": key}))
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE uploads SET status='PROCESSING' WHERE id=$1`, uploadID)
	}
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	s.kickDevelopmentJob(jobID)
	return nil
}
func mapJSON(value any) []byte { data, _ := json.Marshal(value); return data }
func (s *Server) updateBook(w http.ResponseWriter, r *http.Request) {
	var input adminBookInput
	if !httpx.Decode(w, r, &input) {
		return
	}
	if err := validateBookInput(input); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if input.Version == nil {
		httpx.WriteError(w, r, httpx.NewError(422, "VERSION_REQUIRED", "Book version is required."))
		return
	}
	p := principal(r)
	bookID := chi.URLParam(r, "bookID")
	var currentStatus string
	if err := s.store.Pool.QueryRow(r.Context(), `SELECT status FROM books WHERE id=$1`, bookID).Scan(&currentStatus); err != nil {
		httpx.WriteError(w, r, httpx.NewError(404, "BOOK_NOT_FOUND", "Book not found."))
		return
	}
	if input.Status != currentStatus && (input.Status == "PUBLISHED" || input.Status == "ARCHIVED" || currentStatus == "PUBLISHED" || currentStatus == "ARCHIVED") {
		httpx.WriteError(w, r, httpx.NewError(422, "INVALID_STATUS_TRANSITION", "Use the publish or archive action for this status."))
		return
	}
	tag, err := s.store.Pool.Exec(r.Context(), `UPDATE books SET title=$1,short_description=$2,publication_month=$3,publication_year=$4,price_minor=$5,currency=$6,status=$7,prebook_enabled=$8,prebook_discount=$9,version=version+1,updated_at=now() WHERE id=$10 AND version=$11`, strings.TrimSpace(input.Title), strings.TrimSpace(input.ShortDescription), input.PublicationMonth, input.PublicationYear, input.Price.AmountMinor, currency(input.Price.Currency), input.Status, input.Prebook.Enabled, input.Prebook.DiscountPercent, bookID, *input.Version)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.WriteError(w, r, httpx.NewError(409, "CONTENT_VERSION_CHANGED", "The book was changed by another request."))
		return
	}
	if input.CoverUploadRef != nil {
		var key string
		if s.store.Pool.QueryRow(r.Context(), `SELECT object_key FROM uploads WHERE id=$1 AND owner_id=$2 AND kind='BOOK_COVER' AND status='VALID'`, *input.CoverUploadRef, p.AccountID).Scan(&key) != nil {
			httpx.WriteError(w, r, httpx.NewError(422, "COVER_UPLOAD_INVALID", "The cover upload is not valid."))
			return
		}
		_, _ = s.store.Pool.Exec(r.Context(), `UPDATE books SET cover_object_key=$1 WHERE id=$2`, key, bookID)
	}
	if input.ContentUploadRef != nil {
		if err = s.attachContentUpload(r, p.AccountID, bookID, *input.ContentUploadRef); err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	if input.YoutubeAsset != nil {
		_, _ = s.store.Pool.Exec(r.Context(), `INSERT INTO youtube_assets(book_id,song_name,youtube_url,updated_by) VALUES($1,$2,$3,$4) ON CONFLICT(book_id) DO UPDATE SET song_name=excluded.song_name,youtube_url=excluded.youtube_url,status='ACTIVE',updated_at=now(),updated_by=excluded.updated_by`, bookID, input.YoutubeAsset.SongName, input.YoutubeAsset.YoutubeURL, p.AccountID)
	}
	s.adminBook(w, r)
}
func (s *Server) publishBook(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	bookID := chi.URLParam(r, "bookID")
	tx, err := s.store.Pool.BeginTx(r.Context(), pgx.TxOptions{})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var valid bool
	var currentStatus string
	err = tx.QueryRow(r.Context(), `SELECT cover_object_key IS NOT NULL AND EXISTS(SELECT 1 FROM book_content_versions c WHERE c.id=books.current_content_version_id AND c.status='VALID'),status FROM books WHERE id=$1 FOR UPDATE`, bookID).Scan(&valid, &currentStatus)
	if err == nil && currentStatus == "PUBLISHED" {
		httpx.JSON(w, 200, map[string]any{"status": "COMPLETED", "alreadyPublished": true})
		return
	}
	if err == nil && currentStatus == "ARCHIVED" {
		httpx.WriteError(w, r, httpx.NewError(409, "INVALID_STATUS_TRANSITION", "An archived book cannot be published."))
		return
	}
	if err != nil || !valid {
		httpx.WriteError(w, r, httpx.NewError(409, "BOOK_NOT_READY", "A validated cover and EPUB are required before publishing."))
		return
	}
	var months int
	_ = tx.QueryRow(r.Context(), `SELECT latest_duration_months FROM app_settings WHERE id=true`).Scan(&months)
	catalogStatus := "NOT_CONFIGURED"
	if s.cfg.BillingMode == "FREE_LAUNCH" {
		catalogStatus = "ACTIVE"
	}
	_, err = tx.Exec(r.Context(), `UPDATE books SET status='PUBLISHED',published_at=COALESCE(published_at,now()),latest_until=COALESCE(latest_until,now()+make_interval(months => $2)),catalog_status=$3,version=version+1,updated_at=now() WHERE id=$1`, bookID, months, catalogStatus)
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO audit_events(id,actor_id,actor_role,action,target_type,target_id,request_id) VALUES($1,$2,'OWNER','BOOK_PUBLISHED','BOOK',$3,$4)`, store.NewID(), p.AccountID, bookID, requestID(r))
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO entitlements(id,reader_id,book_id,status,source,transaction_id) SELECT p.id,p.reader_id,p.book_id,'ACTIVE','PREBOOK',p.transaction_id FROM prebooks p WHERE p.book_id=$1 AND p.status='ACTIVE' ON CONFLICT(reader_id,book_id) DO UPDATE SET status='ACTIVE'`, bookID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE prebooks SET status='CONVERTED',converted_at=now() WHERE book_id=$1 AND status='ACTIVE'`, bookID)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, 200, map[string]any{"operationId": store.NewID(), "status": "COMPLETED"})
}
func (s *Server) archiveBook(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	bookID := chi.URLParam(r, "bookID")
	var current string
	if err := s.store.Pool.QueryRow(r.Context(), `SELECT status FROM books WHERE id=$1`, bookID).Scan(&current); err != nil {
		httpx.WriteError(w, r, httpx.NewError(404, "BOOK_NOT_FOUND", "Book not found."))
		return
	}
	if current == "ARCHIVED" {
		httpx.JSON(w, 200, map[string]string{"status": "ARCHIVED"})
		return
	}
	tag, err := s.store.Pool.Exec(r.Context(), `UPDATE books SET status='ARCHIVED',archived_at=now(),version=version+1,updated_at=now() WHERE id=$1`, bookID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.WriteError(w, r, httpx.NewError(404, "BOOK_NOT_FOUND", "Book not found."))
		return
	}
	s.store.Audit(r.Context(), p.AccountID, p.Role, "BOOK_ARCHIVED", "BOOK", &bookID, requestID(r), nil)
	httpx.JSON(w, 200, map[string]string{"status": "ARCHIVED"})
}
func (s *Server) deleteBook(w http.ResponseWriter, r *http.Request) {
	bookID := chi.URLParam(r, "bookID")
	tag, err := s.store.Pool.Exec(r.Context(), `DELETE FROM books b WHERE b.id=$1 AND b.status='DRAFT' AND NOT EXISTS(SELECT 1 FROM entitlements e WHERE e.book_id=b.id) AND NOT EXISTS(SELECT 1 FROM prebooks p WHERE p.book_id=b.id) AND NOT EXISTS(SELECT 1 FROM transactions t WHERE t.book_id=b.id)`, bookID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.WriteError(w, r, httpx.NewError(409, "RETENTION_REQUIRED", "Only an unused draft can be permanently deleted."))
		return
	}
	w.WriteHeader(204)
}

func (s *Server) adminBooks(w http.ResponseWriter, r *http.Request) {
	status, query, latest := r.URL.Query().Get("status"), r.URL.Query().Get("query"), r.URL.Query().Get("latest") == "true"
	rows, err := s.store.Pool.Query(r.Context(), `SELECT b.id,b.version,b.title,b.short_description,b.cover_object_key,b.publication_month,b.publication_year,b.price_minor,b.currency,b.status,b.prebook_enabled,b.prebook_discount,b.catalog_status,b.created_at,b.updated_at,(SELECT count(*) FROM entitlements e WHERE e.book_id=b.id AND e.status='ACTIVE'),COALESCE((SELECT sum(t.amount_minor) FROM transactions t WHERE t.book_id=b.id AND t.status='COMPLETED'),0),c.version,c.status,u.file_name,y.song_name,y.youtube_url,y.status FROM books b LEFT JOIN LATERAL (SELECT cv.version,cv.status,cv.source_upload_id FROM book_content_versions cv WHERE cv.book_id=b.id ORDER BY cv.version DESC LIMIT 1) c ON true LEFT JOIN uploads u ON u.id=c.source_upload_id LEFT JOIN youtube_assets y ON y.book_id=b.id WHERE ($1='' OR b.status=$1) AND ($2='' OR b.title ILIKE '%'||$2||'%') AND (NOT $3 OR (b.status='PUBLISHED' AND b.latest_until>now())) ORDER BY b.updated_at DESC`, status, query, latest)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		item, err := s.scanAdminBook(rows)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		items = append(items, item)
	}
	total := len(items)
	httpx.JSON(w, 200, domain.CursorPage[map[string]any]{Items: items, Total: &total})
}

type scanner interface{ Scan(...any) error }

func (s *Server) scanAdminBook(row scanner) (map[string]any, error) {
	var id, title, desc, currency, status, catalog string
	var version, month, year, discount, purchases int
	var price, revenue int64
	var cover *string
	var prebook bool
	var created, updated time.Time
	var contentVersion *int
	var contentStatus, contentFileName, song, youtubeURL, youtubeStatus *string
	err := row.Scan(&id, &version, &title, &desc, &cover, &month, &year, &price, &currency, &status, &prebook, &discount, &catalog, &created, &updated, &purchases, &revenue, &contentVersion, &contentStatus, &contentFileName, &song, &youtubeURL, &youtubeStatus)
	if err != nil {
		return nil, err
	}
	coverURL := ""
	if cover != nil {
		coverURL = s.imageURL(*cover)
	}
	item := map[string]any{"id": id, "version": version, "title": title, "shortDescription": desc, "coverUrl": coverURL, "publicationMonth": month, "publicationYear": year, "price": domain.Money{AmountMinor: price, Currency: currency}, "status": status, "purchaseCount": purchases, "revenue": domain.Money{AmountMinor: revenue, Currency: currency}, "contentStatus": "MISSING", "prebook": map[string]any{"enabled": prebook, "discountPercent": discount, "count": 0}, "catalogStatus": catalog, "createdAt": created, "updatedAt": updated}
	if contentStatus != nil {
		item["contentStatus"] = *contentStatus
		if contentFileName != nil {
			item["contentFileName"] = *contentFileName
		} else {
			item["contentFileName"] = "book-v" + strconv.Itoa(*contentVersion) + ".epub"
		}
	}
	if song != nil {
		item["youtubeAsset"] = map[string]any{"songName": *song, "youtubeUrl": *youtubeURL, "qrStatus": *youtubeStatus}
	}
	return item, nil
}
func (s *Server) adminBook(w http.ResponseWriter, r *http.Request) {
	row := s.store.Pool.QueryRow(r.Context(), `SELECT b.id,b.version,b.title,b.short_description,b.cover_object_key,b.publication_month,b.publication_year,b.price_minor,b.currency,b.status,b.prebook_enabled,b.prebook_discount,b.catalog_status,b.created_at,b.updated_at,(SELECT count(*) FROM entitlements e WHERE e.book_id=b.id AND e.status='ACTIVE'),COALESCE((SELECT sum(t.amount_minor) FROM transactions t WHERE t.book_id=b.id AND t.status='COMPLETED'),0),c.version,c.status,u.file_name,y.song_name,y.youtube_url,y.status FROM books b LEFT JOIN LATERAL (SELECT cv.version,cv.status,cv.source_upload_id FROM book_content_versions cv WHERE cv.book_id=b.id ORDER BY cv.version DESC LIMIT 1) c ON true LEFT JOIN uploads u ON u.id=c.source_upload_id LEFT JOIN youtube_assets y ON y.book_id=b.id WHERE b.id=$1`, chi.URLParam(r, "bookID"))
	item, err := s.scanAdminBook(row)
	if err == pgx.ErrNoRows {
		httpx.WriteError(w, r, httpx.NewError(404, "BOOK_NOT_FOUND", "Book not found."))
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, 200, item)
}

func (s *Server) createUpload(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Kind        string `json:"kind"`
		FileName    string `json:"fileName"`
		ContentType string `json:"contentType"`
		Size        int64  `json:"size"`
		Checksum    string `json:"checksumSha256"`
	}
	if !httpx.Decode(w, r, &input) {
		return
	}
	input.Kind = strings.ToUpper(input.Kind)
	if input.Kind != "EPUB" {
		httpx.WriteError(w, r, httpx.NewError(422, "UPLOAD_KIND_INVALID", "This route accepts EPUB content only. Use the media route for images."))
		return
	}
	if input.ContentType != "application/epub+zip" && !strings.HasSuffix(strings.ToLower(input.FileName), ".epub") {
		httpx.WriteError(w, r, httpx.NewError(422, "UNSUPPORTED_FILE_TYPE", "EPUB uploads must use application/epub+zip."))
		return
	}
	limit := int64(100 << 20)
	if input.Size < 1 || input.Size > limit || len(input.Checksum) != 64 {
		httpx.WriteError(w, r, httpx.NewError(422, "UPLOAD_INVALID", "File size or checksum is invalid."))
		return
	}
	var tracked int64
	_ = s.store.Pool.QueryRow(r.Context(), `SELECT tracked_bytes FROM storage_usage WHERE id=true`).Scan(&tracked)
	if tracked+input.Size > s.cfg.StorageHardLimit {
		httpx.WriteError(w, r, httpx.NewError(507, "STORAGE_LIMIT_REACHED", "Storage capacity is currently exhausted."))
		return
	}
	id := store.NewID()
	bucket := s.objects.PrivateBucket
	key := strings.ToLower(input.Kind) + "/" + id + "/" + url.PathEscape(input.FileName)
	providerID, err := s.objects.CreateMultipart(r.Context(), bucket, key, input.ContentType)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_, err = s.store.Pool.Exec(r.Context(), `INSERT INTO uploads(id,owner_id,kind,file_name,content_type,size_bytes,checksum_sha256,bucket,object_key,provider_upload_id,status,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'UPLOADING',now()+interval '24 hours')`, id, principal(r).AccountID, input.Kind, input.FileName, input.ContentType, input.Size, strings.ToLower(input.Checksum), bucket, key, providerID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	partSize := int64(8 << 20)
	httpx.JSON(w, 201, map[string]any{"uploadId": id, "partSize": partSize, "fileName": input.FileName})
}
func (s *Server) uploadParts(w http.ResponseWriter, r *http.Request) {
	var input struct {
		PartNumbers []int32 `json:"partNumbers"`
	}
	if !httpx.Decode(w, r, &input) {
		return
	}
	var bucket, key, provider string
	err := s.store.Pool.QueryRow(r.Context(), `SELECT bucket,object_key,provider_upload_id FROM uploads WHERE id=$1 AND owner_id=$2 AND status='UPLOADING' AND expires_at>now()`, chi.URLParam(r, "uploadID"), principal(r).AccountID).Scan(&bucket, &key, &provider)
	if err != nil {
		httpx.WriteError(w, r, httpx.NewError(404, "UPLOAD_NOT_FOUND", "Upload not found."))
		return
	}
	parts := []map[string]any{}
	for _, number := range input.PartNumbers {
		if number < 1 || number > 10000 {
			continue
		}
		signed, headers, err := s.objects.PresignPart(r.Context(), bucket, key, provider, number)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
		parts = append(parts, map[string]any{"partNumber": number, "url": signed, "headers": headers})
	}
	httpx.JSON(w, 200, map[string]any{"parts": parts})
}
func (s *Server) completeUpload(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Parts []struct {
			PartNumber int32  `json:"partNumber"`
			ETag       string `json:"etag"`
		} `json:"parts"`
		Checksum string `json:"checksumSha256"`
	}
	if !httpx.Decode(w, r, &input) {
		return
	}
	uploadID := chi.URLParam(r, "uploadID")
	var bucket, key, provider, kind, expected string
	var size int64
	err := s.store.Pool.QueryRow(r.Context(), `SELECT bucket,object_key,provider_upload_id,kind,checksum_sha256,size_bytes FROM uploads WHERE id=$1 AND owner_id=$2 AND status='UPLOADING'`, uploadID, principal(r).AccountID).Scan(&bucket, &key, &provider, &kind, &expected, &size)
	if err != nil {
		httpx.WriteError(w, r, httpx.NewError(404, "UPLOAD_NOT_FOUND", "Upload not found."))
		return
	}
	if !strings.EqualFold(input.Checksum, expected) {
		httpx.WriteError(w, r, httpx.NewError(422, "UPLOAD_CHECKSUM_MISMATCH", "The upload checksum does not match the initiated upload."))
		return
	}
	parts := make([]types.CompletedPart, 0, len(input.Parts))
	for _, part := range input.Parts {
		parts = append(parts, types.CompletedPart{PartNumber: aws.Int32(part.PartNumber), ETag: aws.String(part.ETag)})
	}
	if err = s.objects.CompleteMultipart(r.Context(), bucket, key, provider, parts); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	actualSize, err := s.objects.Head(r.Context(), bucket, key)
	if err != nil || actualSize != size {
		httpx.WriteError(w, r, httpx.NewError(422, "UPLOAD_SIZE_MISMATCH", "Uploaded file size does not match."))
		return
	}
	body, _, _, err := s.objects.Get(r.Context(), bucket, key, nil)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	digest := sha256.New()
	_, copyErr := io.Copy(digest, body)
	_ = body.Close()
	if copyErr != nil {
		httpx.WriteError(w, r, copyErr)
		return
	}
	actualChecksum := hex.EncodeToString(digest.Sum(nil))
	if !strings.EqualFold(actualChecksum, expected) {
		_ = s.objects.Delete(r.Context(), bucket, key)
		_, _ = s.store.Pool.Exec(r.Context(), `UPDATE uploads SET status='FAILED',error_code='UPLOAD_CHECKSUM_MISMATCH' WHERE id=$1`, uploadID)
		httpx.WriteError(w, r, httpx.NewError(422, "UPLOAD_CHECKSUM_MISMATCH", "Uploaded file checksum does not match."))
		return
	}
	status := "VALID"
	if kind == "EPUB" {
		status = "UPLOADED"
	}
	_, err = s.store.Pool.Exec(r.Context(), `UPDATE uploads SET status=$1,completed_at=now() WHERE id=$2`, status, uploadID)
	if err == nil {
		_, _ = s.store.Pool.Exec(r.Context(), `UPDATE storage_usage SET tracked_bytes=tracked_bytes+$1 WHERE id=true`, size)
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, 200, map[string]any{"uploadRef": uploadID, "status": status})
}
func (s *Server) abortUpload(w http.ResponseWriter, r *http.Request) {
	uploadID := chi.URLParam(r, "uploadID")
	var bucket, key, provider string
	err := s.store.Pool.QueryRow(r.Context(), `UPDATE uploads SET status='ABORTED' WHERE id=$1 AND owner_id=$2 AND status IN ('CREATED','UPLOADING') RETURNING bucket,object_key,provider_upload_id`, uploadID, principal(r).AccountID).Scan(&bucket, &key, &provider)
	if err == nil {
		_ = s.objects.AbortMultipart(r.Context(), bucket, key, provider)
	}
	w.WriteHeader(204)
}

func (s *Server) operation(w http.ResponseWriter, r *http.Request) {
	var id, kind, status string
	var progress int
	var result []byte
	var errorCode *string
	var created, updated time.Time
	err := s.store.Pool.QueryRow(r.Context(), `SELECT id,kind,status,progress,result,error_code,created_at,updated_at FROM operations WHERE id=$1`, chi.URLParam(r, "operationID")).Scan(&id, &kind, &status, &progress, &result, &errorCode, &created, &updated)
	if err != nil {
		httpx.WriteError(w, r, httpx.NewError(404, "OPERATION_NOT_FOUND", "Operation not found."))
		return
	}
	var data any
	if len(result) > 0 {
		_ = json.Unmarshal(result, &data)
	}
	httpx.JSON(w, 200, map[string]any{"id": id, "kind": kind, "status": status, "progress": progress, "result": data, "errorCode": errorCode, "createdAt": created, "updatedAt": updated})
}

func (s *Server) putYoutubeAsset(w http.ResponseWriter, r *http.Request) {
	var input struct {
		SongName   string `json:"songName"`
		YoutubeURL string `json:"youtubeUrl"`
	}
	if !httpx.Decode(w, r, &input) {
		return
	}
	if strings.TrimSpace(input.SongName) == "" || !validYouTube(input.YoutubeURL) {
		httpx.WriteError(w, r, httpx.NewError(422, "INVALID_YOUTUBE_URL", "Enter an HTTPS YouTube URL and song name."))
		return
	}
	bookID := chi.URLParam(r, "bookID")
	_, err := s.store.Pool.Exec(r.Context(), `INSERT INTO youtube_assets(book_id,song_name,youtube_url,updated_by) VALUES($1,$2,$3,$4) ON CONFLICT(book_id) DO UPDATE SET song_name=excluded.song_name,youtube_url=excluded.youtube_url,status='ACTIVE',updated_at=now(),updated_by=excluded.updated_by`, bookID, input.SongName, input.YoutubeURL, principal(r).AccountID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, 200, map[string]any{"bookId": bookID, "songName": input.SongName, "youtubeUrl": input.YoutubeURL, "qrStatus": "ACTIVE"})
}
func (s *Server) removeYoutubeAsset(w http.ResponseWriter, r *http.Request) {
	_, err := s.store.Pool.Exec(r.Context(), `DELETE FROM youtube_assets WHERE book_id=$1`, chi.URLParam(r, "bookID"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) adminYoutubeAssets(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.Pool.Query(r.Context(), `SELECT b.id,b.title,b.cover_object_key,y.song_name,y.youtube_url,y.status FROM youtube_assets y JOIN books b ON b.id=y.book_id ORDER BY y.updated_at DESC`)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, title, song, u, status string
		var cover *string
		_ = rows.Scan(&id, &title, &cover, &song, &u, &status)
		coverURL := ""
		if cover != nil {
			coverURL = s.imageURL(*cover)
		}
		items = append(items, map[string]any{"bookId": id, "title": title, "coverUrl": coverURL, "songName": song, "youtubeUrl": u, "qrStatus": status})
	}
	httpx.JSON(w, 200, domain.CursorPage[map[string]any]{Items: items})
}

func (s *Server) paymentStatus(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, 200, map[string]any{"mode": s.cfg.BillingMode, "googlePlay": s.payments.Health(), "alternativeBilling": "NOT_CONFIGURED", "lastCatalogSyncAt": nil})
}
func (s *Server) settlement(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, 200, map[string]any{"provider": "Not configured", "status": "NOT_CONFIGURED", "accountHint": "—", "nextSettlementAt": nil})
}
func (s *Server) bookSales(w http.ResponseWriter, r *http.Request) {
	bookID := chi.URLParam(r, "bookID")
	var count int
	var revenue int64
	_ = s.store.Pool.QueryRow(r.Context(), `SELECT count(*),COALESCE(sum(amount_minor),0) FROM transactions WHERE book_id=$1 AND status='COMPLETED'`, bookID).Scan(&count, &revenue)
	httpx.JSON(w, 200, map[string]any{"bookId": bookID, "purchases": count, "revenue": domain.Money{AmountMinor: revenue, Currency: "INR"}})
}
