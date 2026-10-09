package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/mostlyvers/backend/internal/domain"
)

type Store struct{ Pool *pgxpool.Pool }

func Open(ctx context.Context, url string) (*Store, error) {
	config, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	config.MaxConns = 10
	config.MinConns = 0
	config.MaxConnIdleTime = 2 * time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, err
	}
	if err = pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{Pool: pool}, nil
}
func (s *Store) Close()                         { s.Pool.Close() }
func (s *Store) Ping(ctx context.Context) error { return s.Pool.Ping(ctx) }
func NewID() string {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.NewString()
	}
	return id.String()
}
func NormalizeEmail(value string) string { return strings.ToLower(strings.TrimSpace(value)) }

type Account struct {
	ID, Role, Email, PasswordHash, Status string
	CreatedAt                             time.Time
}

func (s *Store) FindAccount(ctx context.Context, role, email string) (Account, error) {
	var a Account
	err := s.Pool.QueryRow(ctx, `SELECT id,role,email,password_hash,status,created_at FROM accounts WHERE role=$1 AND normalized_email=$2`, role, NormalizeEmail(email)).Scan(&a.ID, &a.Role, &a.Email, &a.PasswordHash, &a.Status, &a.CreatedAt)
	return a, err
}
func (s *Store) AccountByID(ctx context.Context, id string) (Account, error) {
	var a Account
	err := s.Pool.QueryRow(ctx, `SELECT id,role,email,password_hash,status,created_at FROM accounts WHERE id=$1`, id).Scan(&a.ID, &a.Role, &a.Email, &a.PasswordHash, &a.Status, &a.CreatedAt)
	return a, err
}

type Signup struct {
	Email, PasswordHash, Name, Gender, Phone string
	Age                                      int
	Device                                   domain.DeviceContext
}

func (s *Store) CreateReader(ctx context.Context, input Signup) (Account, string, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Account{}, "", err
	}
	defer tx.Rollback(ctx)
	accountID, deviceID := NewID(), NewID()
	now := time.Now().UTC()
	_, err = tx.Exec(ctx, `INSERT INTO accounts(id,role,email,normalized_email,password_hash,status,email_verified_at,created_at,updated_at) VALUES($1,'READER',$2,$3,$4,'ACTIVE',$5,$5,$5)`, accountID, strings.TrimSpace(input.Email), NormalizeEmail(input.Email), input.PasswordHash, now)
	if err != nil {
		return Account{}, "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO reader_profiles(account_id,name,age,gender,phone) VALUES($1,$2,$3,$4,$5)`, accountID, strings.TrimSpace(input.Name), input.Age, input.Gender, strings.TrimSpace(input.Phone))
	if err != nil {
		return Account{}, "", err
	}
	_, err = tx.Exec(ctx, `INSERT INTO devices(id,reader_id,installation_id,display_name,platform,os_version,app_version,package_name,authorized_at) VALUES($1,$2,$3,$4,'ANDROID',$5,$6,$7,$8)`, deviceID, accountID, input.Device.InstallationID, input.Device.DisplayName, input.Device.OSVersion, input.Device.AppVersion, input.Device.PackageName, now)
	if err != nil {
		return Account{}, "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return Account{}, "", err
	}
	return Account{ID: accountID, Role: "READER", Email: strings.TrimSpace(input.Email), PasswordHash: input.PasswordHash, Status: "ACTIVE", CreatedAt: now}, deviceID, nil
}

func (s *Store) ReaderProfile(ctx context.Context, accountID string) (map[string]any, error) {
	var id, name, email, gender, phone string
	var age int
	var picture *string
	var created time.Time
	err := s.Pool.QueryRow(ctx, `SELECT a.id,r.name,r.age,r.gender,a.email,r.phone,r.profile_object_key,a.created_at FROM accounts a JOIN reader_profiles r ON r.account_id=a.id WHERE a.id=$1 AND a.role='READER'`, accountID).Scan(&id, &name, &age, &gender, &email, &phone, &picture, &created)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "name": name, "age": age, "gender": gender, "email": email, "phone": phone, "profilePictureUrl": picture, "createdAt": created}, nil
}
func (s *Store) OwnerProfile(ctx context.Context, accountID string) (domain.Owner, error) {
	var o domain.Owner
	err := s.Pool.QueryRow(ctx, `SELECT a.id,o.name,a.email,o.phone,o.profile_object_key FROM accounts a JOIN owner_profiles o ON o.account_id=a.id WHERE a.id=$1 AND a.role='OWNER'`, accountID).Scan(&o.ID, &o.Name, &o.Email, &o.Phone, &o.ProfilePictureURL)
	return o, err
}

func (s *Store) UpsertLoginDevice(ctx context.Context, readerID string, device domain.DeviceContext) (string, error) {
	id := NewID()
	err := s.Pool.QueryRow(ctx, `INSERT INTO devices(id,reader_id,installation_id,display_name,platform,os_version,app_version,package_name) VALUES($1,$2,$3,$4,'ANDROID',$5,$6,$7) ON CONFLICT(reader_id,installation_id) DO UPDATE SET display_name=excluded.display_name,os_version=excluded.os_version,app_version=excluded.app_version,package_name=excluded.package_name,last_seen_at=now() RETURNING id`, id, readerID, device.InstallationID, device.DisplayName, device.OSVersion, device.AppVersion, device.PackageName).Scan(&id)
	return id, err
}
func (s *Store) DeviceAccess(ctx context.Context, readerID, thisDeviceID string) (domain.DeviceAccess, error) {
	var maximum, used int
	var fee int64
	var currency string
	err := s.Pool.QueryRow(ctx, `SELECT maximum_device_changes,device_change_fee_minor,currency,(SELECT count(*) FROM device_changes WHERE reader_id=$1) FROM app_settings WHERE id=true`, readerID).Scan(&maximum, &fee, &currency, &used)
	if err != nil {
		return domain.DeviceAccess{}, err
	}
	var current domain.CurrentDevice
	var authorized *time.Time
	err = s.Pool.QueryRow(ctx, `SELECT id,display_name,authorized_at FROM devices WHERE reader_id=$1 AND authorized_at IS NOT NULL AND revoked_at IS NULL`, readerID).Scan(&current.ID, &current.DisplayName, &authorized)
	var currentPtr *domain.CurrentDevice
	if err == nil && authorized != nil {
		current.AuthorizedAt = *authorized
		currentPtr = &current
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return domain.DeviceAccess{}, err
	}
	mode := domain.BrowseOnly
	if currentPtr != nil && currentPtr.ID == thisDeviceID {
		mode = domain.Full
	}
	remaining := maximum - used
	if remaining < 0 {
		remaining = 0
	}
	return domain.DeviceAccess{AccessMode: mode, CurrentDevice: currentPtr, ThisDeviceID: thisDeviceID, ChangesUsed: used, ChangesRemaining: remaining, MaximumChanges: maximum, TransferAllowed: remaining > 0, CurrentFee: domain.Money{AmountMinor: fee, Currency: currency}}, nil
}

type SessionRecord struct {
	ID, AccountID, Role, FamilyID, ClientType, DeviceID string
	ExpiresAt                                           time.Time
}

func (s *Store) CreateRefreshSession(ctx context.Context, accountID, role, clientType, deviceID string, hash []byte, expiry time.Time, familyID string) (SessionRecord, error) {
	if familyID == "" {
		familyID = NewID()
	}
	record := SessionRecord{ID: NewID(), AccountID: accountID, Role: role, FamilyID: familyID, ClientType: clientType, DeviceID: deviceID, ExpiresAt: expiry}
	_, err := s.Pool.Exec(ctx, `INSERT INTO auth_sessions(id,account_id,token_hash,family_id,client_type,device_id,expires_at) VALUES($1,$2,$3,$4,$5,NULLIF($6,'')::uuid,$7)`, record.ID, accountID, hash, familyID, clientType, deviceID, expiry)
	return record, err
}
func (s *Store) RotateRefreshSession(ctx context.Context, oldHash, newHash []byte, newExpiry time.Time) (SessionRecord, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return SessionRecord{}, err
	}
	defer tx.Rollback(ctx)
	var old SessionRecord
	var rotated, revoked *time.Time
	err = tx.QueryRow(ctx, `SELECT s.id,s.account_id,a.role,s.family_id,s.client_type,COALESCE(s.device_id::text,''),s.expires_at,s.rotated_at,s.revoked_at FROM auth_sessions s JOIN accounts a ON a.id=s.account_id WHERE token_hash=$1 FOR UPDATE`, oldHash).Scan(&old.ID, &old.AccountID, &old.Role, &old.FamilyID, &old.ClientType, &old.DeviceID, &old.ExpiresAt, &rotated, &revoked)
	if err != nil {
		return SessionRecord{}, err
	}
	if revoked != nil || rotated != nil || time.Now().After(old.ExpiresAt) {
		_, _ = tx.Exec(ctx, `UPDATE auth_sessions SET revoked_at=now() WHERE family_id=$1 AND revoked_at IS NULL`, old.FamilyID)
		_ = tx.Commit(ctx)
		return SessionRecord{}, errors.New("refresh token replayed or expired")
	}
	newRecord := SessionRecord{ID: NewID(), AccountID: old.AccountID, Role: old.Role, FamilyID: old.FamilyID, ClientType: old.ClientType, DeviceID: old.DeviceID, ExpiresAt: newExpiry}
	_, err = tx.Exec(ctx, `UPDATE auth_sessions SET rotated_at=now(),last_used_at=now() WHERE id=$1`, old.ID)
	if err != nil {
		return SessionRecord{}, err
	}
	_, err = tx.Exec(ctx, `INSERT INTO auth_sessions(id,account_id,token_hash,family_id,parent_id,client_type,device_id,expires_at) VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,'')::uuid,$8)`, newRecord.ID, newRecord.AccountID, newHash, newRecord.FamilyID, old.ID, newRecord.ClientType, newRecord.DeviceID, newExpiry)
	if err != nil {
		return SessionRecord{}, err
	}
	return newRecord, tx.Commit(ctx)
}
func (s *Store) RevokeSession(ctx context.Context, sessionID string) error {
	_, err := s.Pool.Exec(ctx, `UPDATE auth_sessions SET revoked_at=now() WHERE id=$1`, sessionID)
	return err
}
func (s *Store) SessionActive(ctx context.Context, sessionID string) bool {
	var active bool
	err := s.Pool.QueryRow(ctx, `SELECT revoked_at IS NULL AND expires_at>now() FROM auth_sessions WHERE id=$1`, sessionID).Scan(&active)
	return err == nil && active
}

func (s *Store) Audit(ctx context.Context, actorID, role, action, targetType string, targetID *string, requestID string, metadata map[string]any) {
	data, _ := json.Marshal(metadata)
	_, _ = s.Pool.Exec(ctx, `INSERT INTO audit_events(id,actor_id,actor_role,action,target_type,target_id,request_id,metadata) VALUES($1,NULLIF($2,'')::uuid,$3,$4,$5,NULLIF($6,'')::uuid,$7,$8)`, NewID(), actorID, role, action, targetType, value(targetID), requestID, data)
}
func value(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
func HashText(value string) []byte     { sum := sha256.Sum256([]byte(value)); return sum[:] }
func IsUniqueViolation(err error) bool { return err != nil && strings.Contains(err.Error(), "23505") }
func IsNotFound(err error) bool        { return errors.Is(err, pgx.ErrNoRows) }
func WrapNotFound(err error, what string) error {
	if IsNotFound(err) {
		return fmt.Errorf("%s not found", what)
	}
	return err
}
