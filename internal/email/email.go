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

type Message struct {
	To, Subject, HTML string
	TemplateParams    map[string]string
}
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

type EmailJS struct {
	ServiceID, TemplateID, PublicKey, PrivateKey string
	endpoint                                     string
	client                                       *http.Client
}

func NewEmailJS(serviceID, templateID, publicKey, privateKey string) *EmailJS {
	return &EmailJS{
		ServiceID: serviceID, TemplateID: templateID, PublicKey: publicKey, PrivateKey: privateKey,
		endpoint: "https://api.emailjs.com/api/v1.0/email/send",
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

func (s *EmailJS) Send(ctx context.Context, m Message) error {
	templateParams := map[string]string{
		"to_email":     m.To,
		"subject":      m.Subject,
		"message_html": m.HTML,
	}
	for key, value := range m.TemplateParams {
		templateParams[key] = value
	}
	payload := map[string]any{
		"service_id":      s.ServiceID,
		"template_id":     s.TemplateID,
		"user_id":         s.PublicKey,
		"template_params": templateParams,
	}
	if s.PrivateKey != "" {
		payload["accessToken"] = s.PrivateKey
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("encode EmailJS request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("EmailJS request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("EmailJS returned %d", response.StatusCode)
	}
	return nil
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
