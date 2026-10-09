package email

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/mail"
	"net/smtp"
	"time"
)

type Message struct{ To, Subject, HTML string }
type Sender interface {
	Send(context.Context, Message) error
}
type Console struct{}

func (Console) Send(_ context.Context, m Message) error {
	slog.Info("development email", "to", m.To, "subject", m.Subject)
	return nil
}

type SMTP struct{ Addr, From string }

func (s SMTP) Send(_ context.Context, m Message) error {
	from := s.From
	if parsed, err := mail.ParseAddress(s.From); err == nil {
		from = parsed.Address
	}
	headers := "From: " + s.From + "\r\nTo: " + m.To + "\r\nSubject: " + m.Subject + "\r\nMIME-Version: 1.0\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n"
	return smtp.SendMail(s.Addr, nil, from, []string{m.To}, []byte(headers+m.HTML))
}

type Resend struct {
	APIKey, From string
	client       *http.Client
}

func NewResend(key, from string) *Resend {
	return &Resend{APIKey: key, From: from, client: &http.Client{Timeout: 10 * time.Second}}
}
func (s *Resend) Send(ctx context.Context, m Message) error {
	body, _ := json.Marshal(map[string]any{"from": s.From, "to": []string{m.To}, "subject": m.Subject, "html": m.HTML})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.resend.com/emails", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.APIKey)
	req.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("resend returned %d", response.StatusCode)
	}
	return nil
}
