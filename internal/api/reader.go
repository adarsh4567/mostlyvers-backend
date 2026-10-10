package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/mostlyvers/backend/internal/domain"
	"github.com/mostlyvers/backend/internal/httpx"
	"github.com/mostlyvers/backend/internal/store"
)

func (s *Server) readerRoutes(r chi.Router) {
	r.Get("/app/bootstrap", s.bootstrap)
	r.Get("/home", s.home)
	r.Get("/books", s.books)
	r.Get("/books/latest", s.latestBooks)
	r.Get("/books/upcoming", s.upcomingBooks)
	r.Get("/books/{bookID}", s.book)
	r.Get("/content/about-app", s.aboutApp)
	r.Get("/content/author", s.authorContent)
	r.Get("/content/contact", s.contactContent)
	r.Get("/me", s.me)
	r.Patch("/me", s.updateMe)
	r.Post("/me/profile-picture", s.updateProfilePicture)
	r.Delete("/me/profile-picture", s.deleteProfilePicture)
	r.Get("/me/device-access", s.deviceAccess)
	r.Post("/me/device-transfer/checkouts", s.paymentUnavailable)
	r.Post("/me/device-transfer/free", s.transferDeviceFree)
	r.Get("/me/device-transfer/checkouts/{checkoutID}", s.paymentUnavailable)
	r.Post("/checkouts", s.paymentUnavailable)
	r.Post("/checkouts/{checkoutID}/confirm", s.paymentUnavailable)
	r.Get("/checkouts/{checkoutID}", s.paymentUnavailable)
	r.Post("/billing/google-play/reconcile", s.paymentUnavailable)
	r.Get("/me/transactions", s.myTransactions)
	r.Get("/me/prebooks", s.myPrebooks)
	r.Get("/me/books", s.myBooks)
	r.Post("/books/{bookID}/claim", s.claimBook)
	r.Post("/books/{bookID}/prebook", s.prebookFree)
	r.Post("/books/{bookID}/reading-sessions", s.createReadingSession)
	r.Get("/reading-sessions/{sessionID}/resources/{resourceID}", s.readResource)
	r.Get("/me/books/{bookID}/progress", s.getProgress)
	r.Put("/me/books/{bookID}/progress", s.putProgress)
	r.Put("/me/books/{bookID}/bookmark", s.putBookmark)
	r.Delete("/me/books/{bookID}/bookmark", s.deleteBookmark)
	r.Post("/me/books/{bookID}/offline-license", s.offlineLicense)
	r.Post("/me/books/{bookID}/offline-license/renew", s.renewOfflineLicense)
	r.Get("/me/books/{bookID}/offline-package/{version}", s.offlinePackage)
	r.Get("/me/downloads", s.downloads)
	r.Delete("/me/downloads/{bookID}", s.deleteDownload)
	r.Get("/me/books/{bookID}/feedback", s.getFeedback)
	r.Post("/me/books/{bookID}/feedback", s.createFeedback)
	r.Patch("/me/books/{bookID}/feedback", s.updateFeedback)
	r.Get("/youtube-assets", s.youtubeAssets)
	r.Get("/books/{bookID}/youtube-asset", s.youtubeAsset)
	r.Post("/me/account-deletion/request", s.authenticatedDeletionRequest)
	r.Post("/me/push-tokens", s.registerPushToken)
	r.Delete("/me/push-tokens", s.unregisterPushToken)
	r.Get("/me/notifications", s.readerNotifications)
	r.Post("/me/notifications/{notificationID}/read", s.readNotification)
	r.Post("/me/notifications/read-all", s.readAllNotifications)
}

func (s *Server) paymentUnavailable(w http.ResponseWriter, r *http.Request) {
	httpx.WriteError(w, r, httpx.NewError(http.StatusServiceUnavailable, "PAYMENT_NOT_CONFIGURED", "Payments are not configured yet."))
}
func (s *Server) sessionDeviceID(ctx context.Context, sessionID string) (string, error) {
	var id string
	err := s.store.Pool.QueryRow(ctx, `SELECT COALESCE(device_id::text,'') FROM auth_sessions WHERE id=$1`, sessionID).Scan(&id)
	return id, err
}
func (s *Server) requireFull(ctx context.Context, p domain.Principal) (string, error) {
	deviceID, err := s.sessionDeviceID(ctx, p.SessionID)
	if err != nil {
		return "", err
	}
	access, err := s.store.DeviceAccess(ctx, p.AccountID, deviceID)
	if err != nil {
		return "", err
	}
	if access.AccessMode != domain.Full {
		return "", httpx.NewError(403, "DEVICE_TRANSFER_REQUIRED", "Authorize this device before using protected content.")
	}
	return deviceID, nil
}
func (s *Server) requireOwned(ctx context.Context, readerID, bookID string) error {
	var exists bool
	err := s.store.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM entitlements WHERE reader_id=$1 AND book_id=$2 AND status='ACTIVE')`, readerID, bookID).Scan(&exists)
	if err != nil {
		return err
	}
	if !exists {
		return httpx.NewError(403, "OWNERSHIP_REQUIRED", "Purchase this book to continue.")
	}
	return nil
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	profile, err := s.store.ReaderProfile(r.Context(), principal(r).AccountID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	s.signReaderPicture(r.Context(), profile)
	httpx.JSON(w, 200, profile)
}
func (s *Server) updateMe(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name   *string `json:"name"`
		Age    *int    `json:"age"`
		Gender *string `json:"gender"`
		Phone  *string `json:"phone"`
	}
	if !httpx.Decode(w, r, &input) {
		return
	}
	p := principal(r)
	current, err := s.store.ReaderProfile(r.Context(), p.AccountID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	name := current["name"].(string)
	age := current["age"].(int)
	gender := current["gender"].(string)
	phone := current["phone"].(string)
	if input.Name != nil {
		name = strings.TrimSpace(*input.Name)
	}
	if input.Age != nil {
		age = *input.Age
	}
	if input.Gender != nil {
		gender = *input.Gender
	}
	if input.Phone != nil {
		phone = strings.TrimSpace(*input.Phone)
	}
	if name == "" || age < 13 || age > 120 || strings.TrimSpace(gender) == "" || phone == "" {
		httpx.WriteError(w, r, httpx.NewError(422, "VALIDATION_ERROR", "Profile fields are invalid."))
		return
	}
	_, err = s.store.Pool.Exec(r.Context(), `UPDATE reader_profiles SET name=$1,age=$2,gender=$3,phone=$4 WHERE account_id=$5`, name, age, gender, phone, p.AccountID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	s.me(w, r)
}
func (s *Server) updateProfilePicture(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 6<<20)
	if err := r.ParseMultipartForm(6 << 20); err != nil {
		httpx.WriteError(w, r, httpx.NewError(413, "FILE_TOO_LARGE", "Profile pictures must be 5 MB or smaller."))
		return
	}
	file, header, err := r.FormFile("profilePicture")
	if err != nil {
		httpx.WriteError(w, r, httpx.NewError(422, "VALIDATION_ERROR", "A profile picture is required."))
		return
	}
	defer file.Close()
	contentType := header.Header.Get("Content-Type")
	if contentType != "image/jpeg" && contentType != "image/png" && contentType != "image/webp" {
		httpx.WriteError(w, r, httpx.NewError(422, "UNSUPPORTED_FILE_TYPE", "Use JPG, PNG or WebP."))
		return
	}
	p := principal(r)
	key, err := s.storeImage(r.Context(), "profiles/readers/"+p.AccountID, header.Filename, contentType, file, header.Size)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var oldKey *string
	_ = s.store.Pool.QueryRow(r.Context(), `SELECT profile_object_key FROM reader_profiles WHERE account_id=$1`, p.AccountID).Scan(&oldKey)
	_, err = s.store.Pool.Exec(r.Context(), `UPDATE reader_profiles SET profile_object_key=$1 WHERE account_id=$2`, key, p.AccountID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if oldKey != nil {
		s.deleteImage(r.Context(), *oldKey)
	}
	s.me(w, r)
}
func (s *Server) deleteProfilePicture(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	var key *string
	_ = s.store.Pool.QueryRow(r.Context(), `SELECT profile_object_key FROM reader_profiles WHERE account_id=$1`, p.AccountID).Scan(&key)
	_, _ = s.store.Pool.Exec(r.Context(), `UPDATE reader_profiles SET profile_object_key=NULL WHERE account_id=$1`, p.AccountID)
	if key != nil {
		s.deleteImage(r.Context(), *key)
	}
	w.WriteHeader(204)
}
func (s *Server) deviceAccess(w http.ResponseWriter, r *http.Request) {
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
	httpx.JSON(w, 200, access)
}

func (s *Server) bootstrap(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	deviceID, _ := s.sessionDeviceID(r.Context(), p.SessionID)
	access, _ := s.store.DeviceAccess(r.Context(), p.AccountID, deviceID)
	var welcome, thanks string
	var fee int64
	var currency string
	var max, discount, duration int
	_ = s.store.Pool.QueryRow(r.Context(), `SELECT welcome_message,thank_you_message,device_change_fee_minor,currency,maximum_device_changes,default_prebook_discount,latest_duration_months FROM app_settings WHERE id=true`).Scan(&welcome, &thanks, &fee, &currency, &max, &discount, &duration)
	httpx.JSON(w, 200, map[string]any{"welcomeMessage": welcome, "thankYouMessage": thanks, "deviceAccess": access, "settings": map[string]any{"billingMode": s.cfg.BillingMode, "deviceChangeFee": domain.Money{AmountMinor: fee, Currency: currency}, "maximumDeviceChanges": max, "defaultPrebookDiscount": discount, "latestDurationMonths": duration}})
}
func (s *Server) aboutApp(w http.ResponseWriter, r *http.Request) {
	var text string
	if err := s.store.Pool.QueryRow(r.Context(), `SELECT about_app FROM app_settings WHERE id=true`).Scan(&text); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, 200, map[string]string{"content": text})
}
func (s *Server) authorContent(w http.ResponseWriter, r *http.Request) {
	author, err := s.authorData(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, 200, author)
}
func (s *Server) authorData(ctx context.Context) (map[string]any, error) {
	var name, short, full string
	var photo *string
	var social []byte
	err := s.store.Pool.QueryRow(ctx, `SELECT name,short_bio,full_bio,photo_object_key,social_links FROM author_profile WHERE id=true`).Scan(&name, &short, &full, &photo, &social)
	if err != nil {
		return nil, err
	}
	links := []any{}
	_ = json.Unmarshal(social, &links)
	photoURL := ""
	if photo != nil {
		photoURL = s.imageURL(*photo)
	}
	return map[string]any{"name": name, "bioSnippet": short, "fullBio": full, "photoUrl": photoURL, "socialLinks": links}, nil
}
func (s *Server) contactContent(w http.ResponseWriter, r *http.Request) {
	var instagramName, instagramURL, youtubeName, youtubeURL, email string
	var support *string
	err := s.store.Pool.QueryRow(r.Context(), `SELECT instagram_username,instagram_url,youtube_name,youtube_url,official_email,support_email FROM contact_settings WHERE id=true`).Scan(&instagramName, &instagramURL, &youtubeName, &youtubeURL, &email, &support)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, 200, map[string]any{"instagramName": instagramName, "instagramUrl": instagramURL, "youtubeName": youtubeName, "youtubeUrl": youtubeURL, "email": email, "supportEmail": support})
}

type bookDTO struct {
	ID, Title, ShortDescription, CoverURL, Status, Ownership, Currency string
	PublicationMonth, PublicationYear                                  int
	PriceMinor                                                         int64
	LatestUntil                                                        *time.Time
	PrebookEnabled                                                     bool
	Discount                                                           int
	PurchaseProductID, PrebookProductID                                *string
	Youtube                                                            bool
}

func (b bookDTO) JSON() map[string]any {
	value := map[string]any{"id": b.ID, "title": b.Title, "shortDescription": b.ShortDescription, "coverUrl": b.CoverURL, "publicationMonth": b.PublicationMonth, "publicationYear": b.PublicationYear, "status": b.Status, "ownership": b.Ownership, "price": domain.Money{AmountMinor: b.PriceMinor, Currency: b.Currency}, "billing": map[string]any{"purchaseProductId": b.PurchaseProductID, "prebookProductId": b.PrebookProductID}, "youtubeAvailability": b.Youtube}
	if b.LatestUntil != nil {
		value["latestUntil"] = b.LatestUntil
	}
	if b.Status == "UPCOMING" {
		price := b.PriceMinor * (100 - int64(b.Discount)) / 100
		value["prebook"] = map[string]any{"enabled": b.PrebookEnabled, "discountPercent": b.Discount, "price": domain.Money{AmountMinor: price, Currency: b.Currency}}
	}
	return value
}
func (s *Server) queryBooks(ctx context.Context, readerID, where string, args ...any) ([]map[string]any, error) {
	suffix := ""
	if index := strings.LastIndex(strings.ToUpper(where), " LIMIT "); index >= 0 {
		suffix = where[index:]
		where = where[:index]
	}
	query := `SELECT b.id,b.title,b.short_description,b.cover_object_key,b.publication_month,b.publication_year,b.status,b.price_minor,b.currency,b.latest_until,b.prebook_enabled,b.prebook_discount,b.purchase_product_id,b.prebook_product_id,EXISTS(SELECT 1 FROM youtube_assets y WHERE y.book_id=b.id AND y.status='ACTIVE'),CASE WHEN EXISTS(SELECT 1 FROM entitlements e WHERE e.reader_id=$1 AND e.book_id=b.id AND e.status='ACTIVE') THEN 'OWNED' WHEN EXISTS(SELECT 1 FROM prebooks p WHERE p.reader_id=$1 AND p.book_id=b.id AND p.status='ACTIVE') THEN 'PREBOOKED' ELSE 'NONE' END FROM books b WHERE ` + where + ` ORDER BY COALESCE(b.published_at,make_date(b.publication_year,b.publication_month,1)) DESC,b.id DESC` + suffix
	allArgs := append([]any{readerID}, args...)
	rows, err := s.store.Pool.Query(ctx, query, allArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var b bookDTO
		var cover *string
		if err = rows.Scan(&b.ID, &b.Title, &b.ShortDescription, &cover, &b.PublicationMonth, &b.PublicationYear, &b.Status, &b.PriceMinor, &b.Currency, &b.LatestUntil, &b.PrebookEnabled, &b.Discount, &b.PurchaseProductID, &b.PrebookProductID, &b.Youtube, &b.Ownership); err != nil {
			return nil, err
		}
		if cover != nil {
			b.CoverURL = s.imageURL(*cover)
		}
		if s.cfg.BillingMode == "FREE_LAUNCH" {
			b.PriceMinor = 0
			b.Currency = "INR"
		}
		items = append(items, b.JSON())
	}
	return items, rows.Err()
}
func (s *Server) books(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	query := strings.TrimSpace(r.URL.Query().Get("query"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 100 {
		limit = 30
	}
	items, err := s.queryBooks(r.Context(), p.AccountID, `b.status='PUBLISHED' AND ($2='' OR b.title ILIKE '%'||$2||'%') LIMIT $3`, query, limit)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, 200, domain.CursorPage[map[string]any]{Items: items, NextCursor: nil})
}
func (s *Server) latestBooks(w http.ResponseWriter, r *http.Request) {
	items, err := s.queryBooks(r.Context(), principal(r).AccountID, `b.status='PUBLISHED' AND b.latest_until>now()`)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, 200, domain.CursorPage[map[string]any]{Items: items})
}
func (s *Server) upcomingBooks(w http.ResponseWriter, r *http.Request) {
	items, err := s.queryBooks(r.Context(), principal(r).AccountID, `b.status='UPCOMING'`)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, 200, domain.CursorPage[map[string]any]{Items: items})
}
func (s *Server) book(w http.ResponseWriter, r *http.Request) {
	items, err := s.queryBooks(r.Context(), principal(r).AccountID, `b.id=$2 AND (b.status IN ('PUBLISHED','UPCOMING') OR EXISTS(SELECT 1 FROM entitlements e WHERE e.reader_id=$1 AND e.book_id=b.id AND e.status='ACTIVE'))`, chi.URLParam(r, "bookID"))
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if len(items) == 0 {
		httpx.WriteError(w, r, httpx.NewError(404, "BOOK_NOT_FOUND", "Book not found."))
		return
	}
	httpx.JSON(w, 200, items[0])
}
func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	latest, _ := s.queryBooks(r.Context(), p.AccountID, `b.status='PUBLISHED' AND b.latest_until>now() LIMIT 1`)
	upcoming, _ := s.queryBooks(r.Context(), p.AccountID, `b.status='UPCOMING' LIMIT 1`)
	library, _ := s.queryBooks(r.Context(), p.AccountID, `b.status='PUBLISHED' LIMIT 5`)
	var welcome, thanks string
	_ = s.store.Pool.QueryRow(r.Context(), `SELECT welcome_message,thank_you_message FROM app_settings WHERE id=true`).Scan(&welcome, &thanks)
	author, err := s.authorData(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	result := map[string]any{"welcomeMessage": welcome, "thankYouMessage": thanks, "libraryPreview": library, "author": author}
	if len(latest) > 0 {
		result["latest"] = latest[0]
	}
	if len(upcoming) > 0 {
		result["upcoming"] = upcoming[0]
	}
	var bookID string
	var href, cfi *string
	var progression, percent float64
	var page, count *int
	var updated time.Time
	var version int
	if err := s.store.Pool.QueryRow(r.Context(), `SELECT p.book_id,p.href,p.cfi,p.progression,p.displayed_page,p.displayed_page_count,p.progress_percent,p.updated_at,p.version FROM reading_progress p JOIN entitlements e ON e.reader_id=p.reader_id AND e.book_id=p.book_id AND e.status='ACTIVE' WHERE p.reader_id=$1 ORDER BY p.updated_at DESC LIMIT 1`, p.AccountID).Scan(&bookID, &href, &cfi, &progression, &page, &count, &percent, &updated, &version); err == nil {
		books, _ := s.queryBooks(r.Context(), p.AccountID, `b.id=$2`, bookID)
		if len(books) > 0 {
			result["continueReading"] = map[string]any{"book": books[0], "progress": domain.ReadingProgress{BookID: bookID, Locator: domain.ReadingLocator{Href: *href, CFI: cfi, Progression: progression, DisplayedPage: page, DisplayedPageCount: count}, ProgressPercent: percent, UpdatedAt: updated, Version: version}}
		}
	}
	httpx.JSON(w, 200, result)
}

func (s *Server) myTransactions(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.Pool.Query(r.Context(), `SELECT t.id,t.type,t.amount_minor,t.currency,t.discount_percent,t.status,t.provider,t.provider_reference,t.created_at,b.id,b.title FROM transactions t LEFT JOIN books b ON b.id=t.book_id WHERE t.reader_id=$1 ORDER BY t.created_at DESC LIMIT 100`, principal(r).AccountID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, kind, currency, status, provider string
		var amount int64
		var discount *int
		var ref, bookID, title *string
		var created time.Time
		if rows.Scan(&id, &kind, &amount, &currency, &discount, &status, &provider, &ref, &created, &bookID, &title) != nil {
			continue
		}
		item := map[string]any{"id": id, "type": kind, "amount": domain.Money{AmountMinor: amount, Currency: currency}, "discountPercent": discount, "status": status, "provider": provider, "providerTransactionReference": ref, "createdAt": created}
		if bookID != nil {
			item["book"] = map[string]any{"id": bookID, "title": title}
		}
		items = append(items, item)
	}
	httpx.JSON(w, 200, domain.CursorPage[map[string]any]{Items: items})
}
func (s *Server) myPrebooks(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.Pool.Query(r.Context(), `SELECT p.id,p.book_id,b.title,p.amount_minor,p.currency,p.discount_percent,p.status,p.created_at FROM prebooks p JOIN books b ON b.id=p.book_id WHERE p.reader_id=$1 ORDER BY p.created_at DESC`, principal(r).AccountID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, bookID, title, currency, status string
		var amount int64
		var discount int
		var created time.Time
		_ = rows.Scan(&id, &bookID, &title, &amount, &currency, &discount, &status, &created)
		items = append(items, map[string]any{"id": id, "book": map[string]any{"id": bookID, "title": title}, "amount": domain.Money{AmountMinor: amount, Currency: currency}, "discountPercent": discount, "status": status, "createdAt": created})
	}
	httpx.JSON(w, 200, domain.CursorPage[map[string]any]{Items: items})
}

func validYouTube(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "youtube.com" || host == "www.youtube.com" || host == "youtu.be" || strings.HasSuffix(host, ".youtube.com")
}
func (s *Server) youtubeAssets(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	deviceID, _ := s.sessionDeviceID(r.Context(), p.SessionID)
	access, _ := s.store.DeviceAccess(r.Context(), p.AccountID, deviceID)
	rows, err := s.store.Pool.Query(r.Context(), `SELECT b.id,b.title,b.cover_object_key,y.song_name,y.youtube_url,EXISTS(SELECT 1 FROM entitlements e WHERE e.reader_id=$1 AND e.book_id=b.id AND e.status='ACTIVE') FROM youtube_assets y JOIN books b ON b.id=y.book_id WHERE y.status='ACTIVE' ORDER BY b.published_at DESC NULLS LAST`, p.AccountID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, title, song, urlValue string
		var cover *string
		var owned bool
		_ = rows.Scan(&id, &title, &cover, &song, &urlValue, &owned)
		unlocked := owned && access.AccessMode == domain.Full
		item := map[string]any{"bookId": id, "title": title, "coverUrl": "", "songName": song, "unlocked": unlocked}
		if cover != nil {
			item["coverUrl"] = s.imageURL(*cover)
		}
		if unlocked {
			item["youtubeUrl"] = urlValue
			item["qrPayload"] = urlValue
		}
		items = append(items, item)
	}
	httpx.JSON(w, 200, domain.CursorPage[map[string]any]{Items: items})
}
func (s *Server) youtubeAsset(w http.ResponseWriter, r *http.Request) {
	bookID := chi.URLParam(r, "bookID")
	p := principal(r)
	if _, err := s.requireFull(r.Context(), p); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	if err := s.requireOwned(r.Context(), p.AccountID, bookID); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var title, song, urlValue string
	var cover *string
	err := s.store.Pool.QueryRow(r.Context(), `SELECT b.title,b.cover_object_key,y.song_name,y.youtube_url FROM youtube_assets y JOIN books b ON b.id=y.book_id WHERE b.id=$1 AND y.status='ACTIVE'`, bookID).Scan(&title, &cover, &song, &urlValue)
	if err != nil {
		httpx.WriteError(w, r, httpx.NewError(404, "YOUTUBE_ASSET_NOT_FOUND", "No YouTube video is associated with this book."))
		return
	}
	item := map[string]any{"bookId": bookID, "title": title, "coverUrl": "", "songName": song, "unlocked": true, "youtubeUrl": urlValue, "qrPayload": urlValue}
	if cover != nil {
		item["coverUrl"] = s.imageURL(*cover)
	}
	httpx.JSON(w, 200, item)
}

func (s *Server) getFeedback(w http.ResponseWriter, r *http.Request) {
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
	var id, text string
	var created, updated time.Time
	err := s.store.Pool.QueryRow(r.Context(), `SELECT id,text,created_at,updated_at FROM feedback WHERE reader_id=$1 AND book_id=$2`, p.AccountID, bookID).Scan(&id, &text, &created, &updated)
	if err == pgx.ErrNoRows {
		httpx.WriteError(w, r, httpx.NewError(404, "FEEDBACK_NOT_FOUND", "Feedback has not been submitted."))
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, 200, map[string]any{"id": id, "bookId": bookID, "text": text, "createdAt": created, "updatedAt": updated})
}
func (s *Server) feedbackText(w http.ResponseWriter, r *http.Request) (string, bool) {
	var input struct {
		Text string `json:"text"`
	}
	if !httpx.Decode(w, r, &input) {
		return "", false
	}
	input.Text = strings.TrimSpace(input.Text)
	if len(input.Text) < 1 || len(input.Text) > 5000 {
		httpx.WriteError(w, r, httpx.NewError(422, "VALIDATION_ERROR", "Feedback must contain 1 to 5000 characters."))
		return "", false
	}
	return input.Text, true
}
func (s *Server) createFeedback(w http.ResponseWriter, r *http.Request) {
	text, ok := s.feedbackText(w, r)
	if !ok {
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
	id := store.NewID()
	var created time.Time
	err := s.store.Pool.QueryRow(r.Context(), `INSERT INTO feedback(id,reader_id,book_id,text) VALUES($1,$2,$3,$4) RETURNING created_at`, id, p.AccountID, bookID, text).Scan(&created)
	if store.IsUniqueViolation(err) {
		httpx.WriteError(w, r, httpx.NewError(409, "FEEDBACK_EXISTS", "Feedback already exists; edit it instead."))
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, 201, map[string]any{"id": id, "bookId": bookID, "text": text, "createdAt": created, "updatedAt": created})
}
func (s *Server) updateFeedback(w http.ResponseWriter, r *http.Request) {
	text, ok := s.feedbackText(w, r)
	if !ok {
		return
	}
	p := principal(r)
	bookID := chi.URLParam(r, "bookID")
	if _, err := s.requireFull(r.Context(), p); err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	var id string
	var created, updated time.Time
	err := s.store.Pool.QueryRow(r.Context(), `UPDATE feedback SET text=$1,updated_at=now() WHERE reader_id=$2 AND book_id=$3 RETURNING id,created_at,updated_at`, text, p.AccountID, bookID).Scan(&id, &created, &updated)
	if err == pgx.ErrNoRows {
		httpx.WriteError(w, r, httpx.NewError(404, "FEEDBACK_NOT_FOUND", "Feedback has not been submitted."))
		return
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, 200, map[string]any{"id": id, "bookId": bookID, "text": text, "createdAt": created, "updatedAt": updated})
}

func queryInt(values map[string]any, key string) int {
	if v, ok := values[key].(float64); ok {
		return int(v)
	}
	return 0
}
func asString(v any) string {
	if value, ok := v.(string); ok {
		return value
	}
	return fmt.Sprint(v)
}
