package api

import (
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mostlyvers/backend/internal/auth"
	emailpkg "github.com/mostlyvers/backend/internal/email"
	"github.com/mostlyvers/backend/internal/httpx"
	"github.com/mostlyvers/backend/internal/store"
)

func (s *Server) authenticatedDeletionRequest(w http.ResponseWriter, r *http.Request) {
	account, err := s.store.AccountByID(r.Context(), principal(r).AccountID)
	if err == nil {
		s.createDeletionRequest(r, account.Email, account.ID)
	}
	httpx.JSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
}

func (s *Server) externalDeletionRequest(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Email string `json:"email"`
	}
	if !httpx.Decode(w, r, &input) {
		return
	}
	account, err := s.store.FindAccount(r.Context(), "READER", input.Email)
	if err == nil && account.Status != "DELETED" {
		s.createDeletionRequest(r, account.Email, account.ID)
	}
	httpx.JSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
}

func (s *Server) createDeletionRequest(r *http.Request, email, accountID string) {
	plain, hash, err := auth.OpaqueToken()
	if err != nil {
		return
	}
	id := store.NewID()
	_, err = s.store.Pool.Exec(r.Context(), `INSERT INTO account_deletion_requests(id,account_id,normalized_email,token_hash,expires_at) VALUES($1,$2,$3,$4,now()+interval '24 hours')`, id, accountID, store.NormalizeEmail(email), hash)
	if err != nil {
		return
	}
	link := cleanURL(s.cfg.PublicBaseURL) + "/account-deletion?token=" + plain
	_ = s.email.Send(r.Context(), emailpkg.Message{To: email, Subject: "Confirm deletion of your MOSTLYVERS account", HTML: `<p>Confirm deletion of your MOSTLYVERS reader account within 24 hours:</p><p><a href="` + template.HTMLEscapeString(link) + `">Confirm account deletion</a></p><p>If you did not request this, ignore this email.</p>`})
}

func (s *Server) deletionTokenInfo(w http.ResponseWriter, r *http.Request) {
	var email, status string
	var expires time.Time
	err := s.store.Pool.QueryRow(r.Context(), `SELECT normalized_email,status,expires_at FROM account_deletion_requests WHERE token_hash=$1`, auth.TokenHash(chi.URLParam(r, "token"))).Scan(&email, &status, &expires)
	if err != nil || status != "PENDING" || time.Now().After(expires) {
		httpx.WriteError(w, r, httpx.NewError(404, "DELETION_TOKEN_INVALID", "The deletion link is invalid or expired."))
		return
	}
	parts := strings.Split(email, "@")
	masked := "***"
	if len(parts) == 2 {
		masked = "***@" + parts[1]
	}
	httpx.JSON(w, 200, map[string]any{"email": masked, "expiresAt": expires, "status": status})
}

func (s *Server) confirmDeletion(w http.ResponseWriter, r *http.Request) {
	tx, err := s.store.Pool.Begin(r.Context())
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var requestID, accountID, email string
	err = tx.QueryRow(r.Context(), `UPDATE account_deletion_requests SET status='CONFIRMED',confirmed_at=now() WHERE token_hash=$1 AND status='PENDING' AND expires_at>now() RETURNING id,account_id,normalized_email`, auth.TokenHash(chi.URLParam(r, "token"))).Scan(&requestID, &accountID, &email)
	if err != nil {
		httpx.WriteError(w, r, httpx.NewError(404, "DELETION_TOKEN_INVALID", "The deletion link is invalid or expired."))
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE accounts SET status='DELETION_PENDING',updated_at=now() WHERE id=$1`, accountID)
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE auth_sessions SET revoked_at=now() WHERE account_id=$1 AND revoked_at IS NULL`, accountID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO jobs(id,kind,payload,run_after) VALUES($1,'DELETE_ACCOUNT',$2,now())`, store.NewID(), mapJSON(map[string]any{"requestId": requestID, "accountId": accountID, "email": email}))
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		httpx.WriteError(w, r, err)
		return
	}
	httpx.JSON(w, 202, map[string]string{"status": "CONFIRMED"})
}

var privacyHTML = template.Must(template.New("privacy").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>MOSTLYVERS Privacy</title><style>body{font:16px system-ui;max-width:760px;margin:48px auto;padding:0 20px;line-height:1.65;color:#29231d}h1{font-family:serif}</style></head><body><h1>MOSTLYVERS Privacy</h1><p>MOSTLYVERS stores account, purchase, device, reading-progress and private-feedback information required to provide the service. Book content and credentials are protected and are not sold.</p><p>You may request deletion from the Android Account screen or the public account-deletion page. Legally required financial and audit records are pseudonymized.</p><p>Contact the support address shown inside the application for privacy questions.</p></body></html>`))
var deletionHTML = template.Must(template.New("deletion").Parse(`<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Delete MOSTLYVERS account</title><style>body{font:16px system-ui;max-width:620px;margin:48px auto;padding:0 20px;line-height:1.6;color:#29231d}input,button{font:inherit;padding:12px;width:100%;box-sizing:border-box;margin:6px 0}button{background:#29231d;color:white;border:0}.danger{background:#8b2020}</style></head><body><h1>Delete your account</h1><div id="request"><p>Enter the email used for your MOSTLYVERS reader account. We will send a single-use confirmation link. Confirmation signs you out immediately and schedules deletion of personal data within 24 hours.</p><form id="f"><input id="email" type="email" required placeholder="Email"><button>Send confirmation link</button></form></div><div id="confirm" hidden><p id="summary"></p><button id="confirmButton" class="danger">Permanently delete my account</button></div><p id="message"></p><script>const token=new URLSearchParams(location.search).get('token');if(token){request.hidden=true;fetch('/v1/account-deletion/requests/'+encodeURIComponent(token)).then(async r=>{if(!r.ok)throw 0;const d=await r.json();confirm.hidden=false;summary.textContent='Confirm permanent deletion for '+d.email+'. This immediately signs out all devices.'}).catch(()=>message.textContent='This deletion link is invalid or expired.');confirmButton.onclick=async()=>{confirmButton.disabled=true;const r=await fetch('/v1/account-deletion/requests/'+encodeURIComponent(token)+'/confirm',{method:'POST'});if(r.ok){confirm.hidden=true;message.textContent='Deletion confirmed. Your personal data will be removed within 24 hours.'}else{message.textContent='This deletion link is invalid or expired.'}}}else{f.onsubmit=async(e)=>{e.preventDefault();await fetch('/v1/account-deletion/request',{method:'POST',headers:{'content-type':'application/json'},body:JSON.stringify({email:email.value})});message.textContent='If an account exists, a confirmation link has been sent.'}}</script></body></html>`))

func (s *Server) privacyPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = privacyHTML.Execute(w, nil)
}
func (s *Server) deletionPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = deletionHTML.Execute(w, nil)
}
