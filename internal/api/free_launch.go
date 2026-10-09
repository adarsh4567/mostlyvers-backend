package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/mostlyvers/backend/internal/httpx"
	"github.com/mostlyvers/backend/internal/store"
)

func (s *Server) requireFreeLaunch(w http.ResponseWriter, r *http.Request) bool {
	if s.cfg.BillingMode == "FREE_LAUNCH" {
		return true
	}
	httpx.WriteError(w, r, httpx.NewError(http.StatusServiceUnavailable, "PAYMENT_NOT_CONFIGURED", "Payments are not configured yet."))
	return false
}

func (s *Server) claimBook(w http.ResponseWriter, r *http.Request) {
	if !s.requireFreeLaunch(w, r) {
		return
	}
	p := principal(r)
	if _, err := s.requireFull(r.Context(), p); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	bookID := chi.URLParam(r, "bookID")
	var available bool
	err := s.store.Pool.QueryRow(r.Context(), `SELECT status='PUBLISHED' AND unavailable_at IS NULL AND EXISTS(SELECT 1 FROM book_content_versions c WHERE c.id=books.current_content_version_id AND c.status='VALID') FROM books WHERE id=$1`, bookID).Scan(&available)
	if err == pgx.ErrNoRows {
		httpx.WriteError(w, r, httpx.NewError(404, "BOOK_NOT_FOUND", "Book not found."))
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if !available {
		httpx.WriteError(w, r, httpx.NewError(409, "BOOK_NOT_RELEASED", "This book is not available yet."))
		return
	}
	_, err = s.store.Pool.Exec(r.Context(), `INSERT INTO entitlements(id,reader_id,book_id,status,source) VALUES($1,$2,$3,'ACTIVE','FREE_LAUNCH') ON CONFLICT(reader_id,book_id) DO UPDATE SET status='ACTIVE',revoked_at=NULL`, store.NewID(), p.AccountID, bookID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	s.store.Audit(r.Context(), p.AccountID, p.Role, "FREE_BOOK_CLAIMED", "BOOK", &bookID, requestID(r), nil)
	s.book(w, r)
}

func (s *Server) prebookFree(w http.ResponseWriter, r *http.Request) {
	if !s.requireFreeLaunch(w, r) {
		return
	}
	p := principal(r)
	if _, err := s.requireFull(r.Context(), p); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	bookID := chi.URLParam(r, "bookID")
	var enabled bool
	var discount int
	var currency string
	err := s.store.Pool.QueryRow(r.Context(), `SELECT status='UPCOMING' AND prebook_enabled,prebook_discount,currency FROM books WHERE id=$1`, bookID).Scan(&enabled, &discount, &currency)
	if err == pgx.ErrNoRows {
		httpx.WriteError(w, r, httpx.NewError(404, "BOOK_NOT_FOUND", "Book not found."))
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if !enabled {
		httpx.WriteError(w, r, httpx.NewError(409, "PREBOOK_NOT_AVAILABLE", "Pre-booking is not available for this book."))
		return
	}
	_, err = s.store.Pool.Exec(r.Context(), `INSERT INTO prebooks(id,reader_id,book_id,transaction_id,amount_minor,currency,discount_percent,status) VALUES($1,$2,$3,NULL,0,$4,$5,'ACTIVE') ON CONFLICT(reader_id,book_id) DO UPDATE SET status='ACTIVE'`, store.NewID(), p.AccountID, bookID, currency, discount)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	s.store.Audit(r.Context(), p.AccountID, p.Role, "FREE_PREBOOK_CREATED", "BOOK", &bookID, requestID(r), nil)
	s.book(w, r)
}

func (s *Server) transferDeviceFree(w http.ResponseWriter, r *http.Request) {
	if !s.requireFreeLaunch(w, r) {
		return
	}
	p := principal(r)
	deviceID, err := s.sessionDeviceID(r.Context(), p.SessionID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	access, err := s.store.DeviceAccess(r.Context(), p.AccountID, deviceID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if access.AccessMode == "FULL" {
		httpx.JSON(w, 200, access)
		return
	}
	if !access.TransferAllowed {
		httpx.WriteError(w, r, httpx.NewError(403, "DEVICE_LIMIT_EXHAUSTED", "The device-change limit has been reached."))
		return
	}
	tx, err := s.store.Pool.BeginTx(r.Context(), pgx.TxOptions{})
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var fromID *string
	_ = tx.QueryRow(r.Context(), `SELECT id FROM devices WHERE reader_id=$1 AND authorized_at IS NOT NULL AND revoked_at IS NULL FOR UPDATE`, p.AccountID).Scan(&fromID)
	if fromID != nil {
		_, err = tx.Exec(r.Context(), `UPDATE devices SET revoked_at=now() WHERE id=$1`, *fromID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE devices SET authorized_at=now(),revoked_at=NULL WHERE id=$1 AND reader_id=$2`, deviceID, p.AccountID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO device_changes(id,reader_id,from_device_id,to_device_id,transaction_id,sequence_number,fee_minor,currency) VALUES($1,$2,$3,$4,NULL,$5,0,$6)`, store.NewID(), p.AccountID, fromID, deviceID, access.ChangesUsed+1, access.CurrentFee.Currency)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	result, err := s.store.DeviceAccess(r.Context(), p.AccountID, deviceID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, 200, result)
}
