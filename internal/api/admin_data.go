package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/mostlyvers/backend/internal/domain"
	"github.com/mostlyvers/backend/internal/httpx"
)

func (s *Server) transactionItems(r *http.Request, limit int) []map[string]any {
	query := `SELECT t.id,t.type,t.status,COALESCE(rp.name,'Deleted reader'),b.title,t.amount_minor,t.currency,t.provider,t.created_at
		FROM transactions t LEFT JOIN reader_profiles rp ON rp.account_id=t.reader_id LEFT JOIN books b ON b.id=t.book_id
		ORDER BY t.created_at DESC LIMIT $1`
	rows, err := s.store.Pool.Query(r.Context(), query, limit)
	if err != nil {
		return []map[string]any{}
	}
	defer rows.Close()
	return scanTransactions(rows)
}

func scanTransactions(rows pgx.Rows) []map[string]any {
	items := []map[string]any{}
	for rows.Next() {
		var id, kind, status, reader, currency, provider string
		var title *string
		var amount int64
		var created time.Time
		if rows.Scan(&id, &kind, &status, &reader, &title, &amount, &currency, &provider, &created) != nil {
			continue
		}
		item := map[string]any{"id": id, "type": kind, "status": status, "readerName": reader, "amount": domain.Money{AmountMinor: amount, Currency: currency}, "provider": provider, "createdAt": created}
		if title != nil {
			item["bookTitle"] = *title
		}
		items = append(items, item)
	}
	return items
}

func (s *Server) feedbackItems(r *http.Request, limit int) []map[string]any {
	rows, err := s.store.Pool.Query(r.Context(), `SELECT f.id,f.book_id,b.title,f.reader_id,COALESCE(rp.name,'Deleted reader'),rp.profile_object_key,f.text,f.created_at,f.updated_at FROM feedback f JOIN books b ON b.id=f.book_id LEFT JOIN reader_profiles rp ON rp.account_id=f.reader_id ORDER BY f.updated_at DESC LIMIT $1`, limit)
	if err != nil {
		return []map[string]any{}
	}
	defer rows.Close()
	return s.scanFeedback(rows)
}

func (s *Server) scanFeedback(rows pgx.Rows) []map[string]any {
	items := []map[string]any{}
	for rows.Next() {
		var id, bookID, bookTitle, readerID, readerName, text string
		var avatar *string
		var created, updated time.Time
		if rows.Scan(&id, &bookID, &bookTitle, &readerID, &readerName, &avatar, &text, &created, &updated) != nil {
			continue
		}
		item := map[string]any{"id": id, "bookId": bookID, "bookTitle": bookTitle, "readerId": readerID, "readerName": readerName, "text": text, "createdAt": created, "updatedAt": updated}
		if avatar != nil {
			item["readerAvatar"] = s.imageURL(*avatar)
		}
		items = append(items, item)
	}
	return items
}

func (s *Server) adminReaders(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.Pool.Query(r.Context(), `SELECT a.id,rp.name,rp.age,rp.gender,a.email,rp.phone,rp.profile_object_key,a.created_at,
		(SELECT count(*) FROM entitlements e WHERE e.reader_id=a.id AND e.status='ACTIVE'),
		(SELECT d.display_name FROM devices d WHERE d.reader_id=a.id AND d.authorized_at IS NOT NULL AND d.revoked_at IS NULL LIMIT 1),
		(SELECT count(*) FROM device_changes dc WHERE dc.reader_id=a.id),s.maximum_device_changes,
		(SELECT count(*) FROM downloads dl WHERE dl.reader_id=a.id AND dl.removed_at IS NULL)
		FROM accounts a JOIN reader_profiles rp ON rp.account_id=a.id CROSS JOIN app_settings s WHERE a.role='READER' AND a.status<>'DELETED' ORDER BY a.created_at DESC`)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		if item, err := s.scanReader(r.Context(), rows); err == nil {
			items = append(items, item)
		}
	}
	total := len(items)
	httpx.JSON(w, 200, domain.CursorPage[map[string]any]{Items: items, Total: &total})
}

func (s *Server) scanReader(ctx context.Context, row scanner) (map[string]any, error) {
	var id, name, gender, email, phone string
	var age, books, used, maximum, downloads int
	var photo, current *string
	var created time.Time
	err := row.Scan(&id, &name, &age, &gender, &email, &phone, &photo, &created, &books, &current, &used, &maximum, &downloads)
	if err != nil {
		return nil, err
	}
	item := map[string]any{"id": id, "name": name, "age": age, "gender": gender, "email": email, "phone": phone, "createdAt": created, "purchasedBooks": books, "changesUsed": used, "changesRemaining": max(0, maximum-used), "downloads": downloads}
	if photo != nil {
		if signed, signErr := s.objects.PresignGet(ctx, s.objects.PrivateBucket, *photo); signErr == nil {
			item["profilePictureUrl"] = signed
		}
	}
	if current != nil {
		item["currentDevice"] = *current
	}
	return item, nil
}

func (s *Server) adminReader(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "readerID")
	row := s.store.Pool.QueryRow(r.Context(), `SELECT a.id,rp.name,rp.age,rp.gender,a.email,rp.phone,rp.profile_object_key,a.created_at,(SELECT count(*) FROM entitlements e WHERE e.reader_id=a.id AND e.status='ACTIVE'),(SELECT d.display_name FROM devices d WHERE d.reader_id=a.id AND d.authorized_at IS NOT NULL AND d.revoked_at IS NULL LIMIT 1),(SELECT count(*) FROM device_changes dc WHERE dc.reader_id=a.id),s.maximum_device_changes,(SELECT count(*) FROM downloads dl WHERE dl.reader_id=a.id AND dl.removed_at IS NULL) FROM accounts a JOIN reader_profiles rp ON rp.account_id=a.id CROSS JOIN app_settings s WHERE a.id=$1 AND a.role='READER'`, id)
	item, err := s.scanReader(r.Context(), row)
	if err != nil {
		httpx.WriteError(w, r, httpx.NewError(404, "READER_NOT_FOUND", "Reader not found."))
		return
	}
	books, _ := s.queryBooks(r.Context(), id, `EXISTS(SELECT 1 FROM entitlements e WHERE e.reader_id=$1 AND e.book_id=b.id AND e.status='ACTIVE')`)
	item["books"] = books
	rows, err := s.store.Pool.Query(r.Context(), `SELECT t.id,t.type,t.status,COALESCE(rp.name,'Deleted reader'),b.title,t.amount_minor,t.currency,t.provider,t.created_at FROM transactions t LEFT JOIN reader_profiles rp ON rp.account_id=t.reader_id LEFT JOIN books b ON b.id=t.book_id WHERE t.reader_id=$1 ORDER BY t.created_at DESC`, id)
	if err == nil {
		item["transactions"] = scanTransactions(rows)
		rows.Close()
	} else {
		item["transactions"] = []any{}
	}
	rows, err = s.store.Pool.Query(r.Context(), `SELECT f.id,f.book_id,b.title,f.reader_id,COALESCE(rp.name,'Deleted reader'),rp.profile_object_key,f.text,f.created_at,f.updated_at FROM feedback f JOIN books b ON b.id=f.book_id LEFT JOIN reader_profiles rp ON rp.account_id=f.reader_id WHERE f.reader_id=$1 ORDER BY f.updated_at DESC`, id)
	if err == nil {
		item["feedback"] = s.scanFeedback(rows)
		rows.Close()
	} else {
		item["feedback"] = []any{}
	}
	httpx.JSON(w, 200, item)
}

func (s *Server) adminTransactions(w http.ResponseWriter, r *http.Request) {
	kind, status := r.URL.Query().Get("type"), r.URL.Query().Get("status")
	rows, err := s.store.Pool.Query(r.Context(), `SELECT t.id,t.type,t.status,COALESCE(rp.name,'Deleted reader'),b.title,t.amount_minor,t.currency,t.provider,t.created_at FROM transactions t LEFT JOIN reader_profiles rp ON rp.account_id=t.reader_id LEFT JOIN books b ON b.id=t.book_id WHERE ($1='' OR t.type=$1) AND ($2='' OR t.status=$2) ORDER BY t.created_at DESC LIMIT 500`, kind, status)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	items := scanTransactions(rows)
	total := len(items)
	httpx.JSON(w, 200, domain.CursorPage[map[string]any]{Items: items, Total: &total})
}

func (s *Server) adminTransaction(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.Pool.Query(r.Context(), `SELECT t.id,t.type,t.status,COALESCE(rp.name,'Deleted reader'),b.title,t.amount_minor,t.currency,t.provider,t.created_at FROM transactions t LEFT JOIN reader_profiles rp ON rp.account_id=t.reader_id LEFT JOIN books b ON b.id=t.book_id WHERE t.id=$1`, chi.URLParam(r, "transactionID"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	items := scanTransactions(rows)
	if len(items) == 0 {
		httpx.WriteError(w, r, httpx.NewError(404, "TRANSACTION_NOT_FOUND", "Transaction not found."))
		return
	}
	httpx.JSON(w, 200, items[0])
}

func (s *Server) salesSummary(w http.ResponseWriter, r *http.Request) {
	var total, completed, refunded int
	var revenue, refunds int64
	err := s.store.Pool.QueryRow(r.Context(), `SELECT count(*),count(*) FILTER(WHERE status='COMPLETED'),count(*) FILTER(WHERE status='REFUNDED'),COALESCE(sum(amount_minor) FILTER(WHERE status='COMPLETED'),0),COALESCE(sum(amount_minor) FILTER(WHERE status='REFUNDED'),0) FROM transactions`).Scan(&total, &completed, &refunded, &revenue, &refunds)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, 200, map[string]any{"total": total, "completed": completed, "refunded": refunded, "revenue": domain.Money{AmountMinor: revenue, Currency: "INR"}, "refunds": domain.Money{AmountMinor: refunds, Currency: "INR"}})
}
func (s *Server) salesTimeseries(w http.ResponseWriter, r *http.Request) {
	httpx.JSON(w, 200, map[string]any{"items": s.revenueSeries(r)})
}
func (s *Server) salesExport(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.Pool.Query(r.Context(), `SELECT t.id,t.type,t.status,COALESCE(rp.name,'Deleted reader'),COALESCE(b.title,''),t.amount_minor,t.currency,t.provider,t.created_at FROM transactions t LEFT JOIN reader_profiles rp ON rp.account_id=t.reader_id LEFT JOIN books b ON b.id=t.book_id ORDER BY t.created_at DESC`)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="mostlyvers-transactions.csv"`)
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"id", "type", "status", "reader", "book", "amount_minor", "currency", "provider", "created_at"})
	for rows.Next() {
		var id, kind, status, reader, book, currency, provider string
		var amount int64
		var created time.Time
		if rows.Scan(&id, &kind, &status, &reader, &book, &amount, &currency, &provider, &created) == nil {
			_ = writer.Write([]string{id, kind, status, reader, book, strconv.FormatInt(amount, 10), currency, provider, created.Format(time.RFC3339)})
		}
	}
	writer.Flush()
}

func (s *Server) adminFeedback(w http.ResponseWriter, r *http.Request) {
	items := s.feedbackItems(r, 500)
	total := len(items)
	httpx.JSON(w, 200, domain.CursorPage[map[string]any]{Items: items, Total: &total})
}
func (s *Server) adminFeedbackItem(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.Pool.Query(r.Context(), `SELECT f.id,f.book_id,b.title,f.reader_id,COALESCE(rp.name,'Deleted reader'),rp.profile_object_key,f.text,f.created_at,f.updated_at FROM feedback f JOIN books b ON b.id=f.book_id LEFT JOIN reader_profiles rp ON rp.account_id=f.reader_id WHERE f.id=$1`, chi.URLParam(r, "feedbackID"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	items := s.scanFeedback(rows)
	if len(items) == 0 {
		httpx.WriteError(w, r, httpx.NewError(404, "FEEDBACK_NOT_FOUND", "Feedback not found."))
		return
	}
	httpx.JSON(w, 200, items[0])
}

type authorInput struct {
	Version        *int                `json:"version"`
	Name           string              `json:"name"`
	ShortBio       string              `json:"shortBio"`
	FullBio        string              `json:"fullBio"`
	PhotoUploadRef *string             `json:"photoUploadRef"`
	SocialLinks    []map[string]string `json:"socialLinks"`
}

func (s *Server) adminAuthor(w http.ResponseWriter, r *http.Request) {
	var version int
	var name, shortBio, fullBio string
	var photo *string
	var links []byte
	err := s.store.Pool.QueryRow(r.Context(), `SELECT version,name,short_bio,full_bio,photo_object_key,social_links FROM author_profile WHERE id=true`).Scan(&version, &name, &shortBio, &fullBio, &photo, &links)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	photoURL := ""
	if photo != nil {
		photoURL = s.imageURL(*photo)
	}
	var social any = []any{}
	_ = json.Unmarshal(links, &social)
	httpx.JSON(w, 200, map[string]any{"version": version, "name": name, "shortBio": shortBio, "fullBio": fullBio, "photoUrl": photoURL, "socialLinks": social})
}
func (s *Server) updateAdminAuthor(w http.ResponseWriter, r *http.Request) {
	var in authorInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		httpx.WriteError(w, r, httpx.NewError(422, "VALIDATION_ERROR", "Author name is required."))
		return
	}
	var photo *string
	if in.PhotoUploadRef != nil {
		_ = s.store.Pool.QueryRow(r.Context(), `SELECT object_key FROM uploads WHERE id=$1 AND owner_id=$2 AND kind='AUTHOR_PHOTO' AND status='VALID'`, *in.PhotoUploadRef, principal(r).AccountID).Scan(&photo)
	}
	tag, err := s.store.Pool.Exec(r.Context(), `UPDATE author_profile SET name=$1,short_bio=$2,full_bio=$3,photo_object_key=COALESCE($4,photo_object_key),social_links=$5,version=version+1,updated_at=now(),updated_by=$6 WHERE id=true AND ($7::integer IS NULL OR version=$7)`, in.Name, in.ShortBio, in.FullBio, photo, mapJSON(in.SocialLinks), principal(r).AccountID, in.Version)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.WriteError(w, r, httpx.NewError(409, "CONTENT_VERSION_CHANGED", "Author content was changed by another request."))
		return
	}
	s.adminAuthor(w, r)
}

type contactInput struct {
	Version           *int    `json:"version"`
	InstagramUsername string  `json:"instagramUsername"`
	InstagramURL      string  `json:"instagramUrl"`
	YoutubeName       string  `json:"youtubeName"`
	YoutubeURL        string  `json:"youtubeUrl"`
	OfficialEmail     string  `json:"officialEmail"`
	SupportEmail      *string `json:"supportEmail"`
}

func (s *Server) adminContact(w http.ResponseWriter, r *http.Request) {
	var version int
	var in contactInput
	err := s.store.Pool.QueryRow(r.Context(), `SELECT version,instagram_username,instagram_url,youtube_name,youtube_url,official_email,support_email FROM contact_settings WHERE id=true`).Scan(&version, &in.InstagramUsername, &in.InstagramURL, &in.YoutubeName, &in.YoutubeURL, &in.OfficialEmail, &in.SupportEmail)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, 200, map[string]any{"version": version, "instagramUsername": in.InstagramUsername, "instagramUrl": in.InstagramURL, "youtubeName": in.YoutubeName, "youtubeUrl": in.YoutubeURL, "officialEmail": in.OfficialEmail, "supportEmail": in.SupportEmail})
}
func (s *Server) updateAdminContact(w http.ResponseWriter, r *http.Request) {
	var in contactInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	if in.OfficialEmail != "" && !validateEmail(in.OfficialEmail) {
		httpx.WriteError(w, r, httpx.NewError(422, "VALIDATION_ERROR", "Official email is invalid."))
		return
	}
	tag, err := s.store.Pool.Exec(r.Context(), `UPDATE contact_settings SET instagram_username=$1,instagram_url=$2,youtube_name=$3,youtube_url=$4,official_email=$5,support_email=$6,version=version+1,updated_at=now(),updated_by=$7 WHERE id=true AND ($8::integer IS NULL OR version=$8)`, in.InstagramUsername, in.InstagramURL, in.YoutubeName, in.YoutubeURL, in.OfficialEmail, in.SupportEmail, principal(r).AccountID, in.Version)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.WriteError(w, r, httpx.NewError(409, "CONTENT_VERSION_CHANGED", "Contact content was changed by another request."))
		return
	}
	s.adminContact(w, r)
}

func (s *Server) adminAboutApp(w http.ResponseWriter, r *http.Request) {
	var text string
	var version int
	err := s.store.Pool.QueryRow(r.Context(), `SELECT about_app,version FROM app_settings WHERE id=true`).Scan(&text, &version)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, 200, map[string]any{"text": text, "version": version})
}
func (s *Server) updateAdminAboutApp(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text     string `json:"text"`
		AboutApp string `json:"aboutApp"`
		Version  *int   `json:"version"`
	}
	if !httpx.Decode(w, r, &in) {
		return
	}
	if in.Text == "" {
		in.Text = in.AboutApp
	}
	tag, err := s.store.Pool.Exec(r.Context(), `UPDATE app_settings SET about_app=$1,version=version+1,updated_at=now(),updated_by=$2 WHERE id=true AND ($3::integer IS NULL OR version=$3)`, in.Text, principal(r).AccountID, in.Version)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.WriteError(w, r, httpx.NewError(409, "CONTENT_VERSION_CHANGED", "App content was changed by another request."))
		return
	}
	s.adminAboutApp(w, r)
}

type settingsInput struct {
	Version                *int         `json:"version"`
	WelcomeMessage         string       `json:"welcomeMessage"`
	ThankYouMessage        string       `json:"thankYouMessage"`
	AboutApp               string       `json:"aboutApp"`
	DeviceChangeFee        domain.Money `json:"deviceChangeFee"`
	MaximumDeviceChanges   int          `json:"maximumDeviceChanges"`
	DefaultPrebookDiscount int          `json:"defaultPrebookDiscount"`
	LatestDurationMonths   int          `json:"latestDurationMonths"`
}

func (s *Server) adminSettings(w http.ResponseWriter, r *http.Request) {
	var in settingsInput
	var version int
	err := s.store.Pool.QueryRow(r.Context(), `SELECT version,welcome_message,thank_you_message,about_app,device_change_fee_minor,currency,maximum_device_changes,default_prebook_discount,latest_duration_months FROM app_settings WHERE id=true`).Scan(&version, &in.WelcomeMessage, &in.ThankYouMessage, &in.AboutApp, &in.DeviceChangeFee.AmountMinor, &in.DeviceChangeFee.Currency, &in.MaximumDeviceChanges, &in.DefaultPrebookDiscount, &in.LatestDurationMonths)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, 200, map[string]any{"version": version, "welcomeMessage": in.WelcomeMessage, "thankYouMessage": in.ThankYouMessage, "aboutApp": in.AboutApp, "deviceChangeFee": in.DeviceChangeFee, "maximumDeviceChanges": in.MaximumDeviceChanges, "defaultPrebookDiscount": in.DefaultPrebookDiscount, "latestDurationMonths": in.LatestDurationMonths})
}
func (s *Server) updateAdminSettings(w http.ResponseWriter, r *http.Request) {
	var in settingsInput
	if !httpx.Decode(w, r, &in) {
		return
	}
	if in.DeviceChangeFee.AmountMinor < 0 || in.MaximumDeviceChanges < 0 || in.DefaultPrebookDiscount < 0 || in.DefaultPrebookDiscount > 90 || in.LatestDurationMonths < 1 {
		httpx.WriteError(w, r, httpx.NewError(422, "VALIDATION_ERROR", "One or more settings are invalid."))
		return
	}
	tag, err := s.store.Pool.Exec(r.Context(), `UPDATE app_settings SET welcome_message=$1,thank_you_message=$2,about_app=$3,device_change_fee_minor=$4,currency=$5,maximum_device_changes=$6,default_prebook_discount=$7,latest_duration_months=$8,version=version+1,updated_at=now(),updated_by=$9 WHERE id=true AND ($10::integer IS NULL OR version=$10)`, in.WelcomeMessage, in.ThankYouMessage, in.AboutApp, in.DeviceChangeFee.AmountMinor, currency(in.DeviceChangeFee.Currency), in.MaximumDeviceChanges, in.DefaultPrebookDiscount, in.LatestDurationMonths, principal(r).AccountID, in.Version)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.WriteError(w, r, httpx.NewError(409, "CONTENT_VERSION_CHANGED", "Settings were changed by another request."))
		return
	}
	s.adminSettings(w, r)
}
