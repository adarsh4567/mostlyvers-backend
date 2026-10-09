package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/mostlyvers/backend/internal/domain"
	"github.com/mostlyvers/backend/internal/httpx"
	"github.com/mostlyvers/backend/internal/store"
)

type notificationInput struct {
	ReaderID *string        `json:"readerId"`
	Title    string         `json:"title"`
	Body     string         `json:"body"`
	Data     map[string]any `json:"data"`
}

func (s *Server) registerPushToken(w http.ResponseWriter, r *http.Request) {
	var input struct{ Token, Provider string }
	if !httpx.Decode(w, r, &input) {
		return
	}
	input.Token, input.Provider = strings.TrimSpace(input.Token), strings.ToUpper(strings.TrimSpace(input.Provider))
	if input.Provider != "EXPO" && input.Provider != "FCM" || len(input.Token) < 16 {
		httpx.WriteError(w, r, httpx.NewError(422, "PUSH_TOKEN_INVALID", "The push token is invalid."))
		return
	}
	p := principal(r)
	deviceID, err := s.requireFull(r.Context(), p)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	_, err = s.store.Pool.Exec(r.Context(), `INSERT INTO push_tokens(id,reader_id,device_id,token,provider) VALUES($1,$2,$3,$4,$5) ON CONFLICT(token) DO UPDATE SET reader_id=excluded.reader_id,device_id=excluded.device_id,provider=excluded.provider,enabled=true,last_error=NULL,updated_at=now()`, store.NewID(), p.AccountID, deviceID, input.Token, input.Provider)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) unregisterPushToken(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Token string `json:"token"`
	}
	if !httpx.Decode(w, r, &input) {
		return
	}
	_, _ = s.store.Pool.Exec(r.Context(), `UPDATE push_tokens SET enabled=false,updated_at=now() WHERE reader_id=$1 AND token=$2`, principal(r).AccountID, input.Token)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) readerNotifications(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.Pool.Query(r.Context(), `SELECT id,title,body,data,read_at,created_at FROM notifications WHERE reader_id=$1 ORDER BY created_at DESC LIMIT 100`, principal(r).AccountID)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	items, unread := []map[string]any{}, 0
	for rows.Next() {
		var id, title, body string
		var data []byte
		var readAt, createdAt any
		if rows.Scan(&id, &title, &body, &data, &readAt, &createdAt) != nil {
			continue
		}
		var payload map[string]any
		_ = json.Unmarshal(data, &payload)
		if readAt == nil {
			unread++
		}
		items = append(items, map[string]any{"id": id, "title": title, "body": body, "data": payload, "readAt": readAt, "createdAt": createdAt})
	}
	httpx.JSON(w, 200, map[string]any{"items": items, "nextCursor": nil, "unreadCount": unread})
}

func (s *Server) readNotification(w http.ResponseWriter, r *http.Request) {
	_, _ = s.store.Pool.Exec(r.Context(), `UPDATE notifications SET read_at=COALESCE(read_at,now()) WHERE id=$1 AND reader_id=$2`, chi.URLParam(r, "notificationID"), principal(r).AccountID)
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) readAllNotifications(w http.ResponseWriter, r *http.Request) {
	_, _ = s.store.Pool.Exec(r.Context(), `UPDATE notifications SET read_at=now() WHERE reader_id=$1 AND read_at IS NULL`, principal(r).AccountID)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) adminNotifications(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.Pool.Query(r.Context(), `SELECT title,body,data,min(created_at),count(*) FROM notifications GROUP BY campaign_id,title,body,data ORDER BY min(created_at) DESC LIMIT 100`)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var title, body string
		var data []byte
		var created any
		var recipients int
		if rows.Scan(&title, &body, &data, &created, &recipients) != nil {
			continue
		}
		var payload map[string]any
		_ = json.Unmarshal(data, &payload)
		items = append(items, map[string]any{"title": title, "body": body, "data": payload, "createdAt": created, "recipientCount": recipients})
	}
	httpx.JSON(w, 200, domain.CursorPage[map[string]any]{Items: items})
}

func (s *Server) sendAdminNotification(w http.ResponseWriter, r *http.Request) {
	var input notificationInput
	if !httpx.Decode(w, r, &input) {
		return
	}
	input.Title, input.Body = strings.TrimSpace(input.Title), strings.TrimSpace(input.Body)
	if input.Title == "" || len(input.Title) > 120 || input.Body == "" || len(input.Body) > 500 {
		httpx.WriteError(w, r, httpx.NewError(422, "VALIDATION_ERROR", "Notification title or message is invalid."))
		return
	}
	data, _ := json.Marshal(input.Data)
	query := `SELECT id FROM accounts WHERE role='READER' AND status='ACTIVE'`
	args := []any{}
	if input.ReaderID != nil {
		query += ` AND id=$1`
		args = append(args, *input.ReaderID)
	}
	rows, err := s.store.Pool.Query(r.Context(), query, args...)
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	readerIDs := []string{}
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			readerIDs = append(readerIDs, id)
		}
	}
	rows.Close()
	campaignID := store.NewID()
	for _, readerID := range readerIDs {
		_, err = s.store.Pool.Exec(r.Context(), `INSERT INTO notifications(id,campaign_id,reader_id,title,body,data) VALUES($1,$2,$3,$4,$5,$6)`, store.NewID(), campaignID, readerID, input.Title, input.Body, data)
		if err != nil {
			httpx.WriteError(w, r, err)
			return
		}
	}
	go s.deliverExpoPush(input, readerIDs)
	httpx.JSON(w, 201, map[string]any{"recipientCount": len(readerIDs)})
}

func (s *Server) deliverExpoPush(input notificationInput, readerIDs []string) {
	if len(readerIDs) == 0 {
		return
	}
	ctx := context.Background()
	rows, err := s.store.Pool.Query(ctx, `SELECT id,token FROM push_tokens WHERE provider='EXPO' AND enabled=true AND reader_id=ANY($1::uuid[])`, readerIDs)
	if err != nil {
		return
	}
	type tokenRow struct{ id, token string }
	tokens := []tokenRow{}
	for rows.Next() {
		var item tokenRow
		if rows.Scan(&item.id, &item.token) == nil {
			tokens = append(tokens, item)
		}
	}
	rows.Close()
	for _, token := range tokens {
		payload, _ := json.Marshal(map[string]any{"to": token.token, "title": input.Title, "body": input.Body, "data": input.Data, "sound": "default"})
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.ExpoPushEndpoint, bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		response, requestErr := http.DefaultClient.Do(req)
		if requestErr != nil {
			_, _ = s.store.Pool.Exec(ctx, `UPDATE push_tokens SET last_error=$2,updated_at=now() WHERE id=$1`, token.id, requestErr.Error())
			continue
		}
		_ = response.Body.Close()
		if response.StatusCode/100 != 2 {
			_, _ = s.store.Pool.Exec(ctx, `UPDATE push_tokens SET last_error=$2,updated_at=now() WHERE id=$1`, token.id, fmt.Sprintf("Expo returned %d", response.StatusCode))
		}
	}
}
