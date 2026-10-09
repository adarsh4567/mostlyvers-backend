package domain

import "time"

type Money struct {
	AmountMinor int64  `json:"amountMinor"`
	Currency    string `json:"currency"`
}
type AccessMode string

const (
	Full       AccessMode = "FULL"
	BrowseOnly AccessMode = "BROWSE_ONLY"
)

type Reader struct {
	ID                string    `json:"id"`
	Name              string    `json:"name"`
	Age               int       `json:"age"`
	Gender            string    `json:"gender"`
	Email             string    `json:"email"`
	Phone             string    `json:"phone"`
	ProfilePictureURL *string   `json:"profilePictureUrl,omitempty"`
	CreatedAt         time.Time `json:"createdAt"`
}
type Owner struct {
	ID                string  `json:"id"`
	Name              string  `json:"name"`
	Email             string  `json:"email"`
	Phone             string  `json:"phone"`
	ProfilePictureURL *string `json:"profilePictureUrl,omitempty"`
}
type DeviceContext struct {
	InstallationID       string `json:"installationId"`
	DisplayName          string `json:"displayName"`
	Platform             string `json:"platform"`
	OSVersion            string `json:"osVersion"`
	AppVersion           string `json:"appVersion"`
	PackageName          string `json:"packageName"`
	IntegrityToken       string `json:"integrityToken,omitempty"`
	IntegrityRequestHash string `json:"integrityRequestHash,omitempty"`
}
type DeviceAccess struct {
	AccessMode       AccessMode     `json:"accessMode"`
	CurrentDevice    *CurrentDevice `json:"currentDevice,omitempty"`
	ThisDeviceID     string         `json:"thisDeviceId"`
	ChangesUsed      int            `json:"changesUsed"`
	ChangesRemaining int            `json:"changesRemaining"`
	MaximumChanges   int            `json:"maximumChanges"`
	TransferAllowed  bool           `json:"transferAllowed"`
	CurrentFee       Money          `json:"currentFee"`
}
type CurrentDevice struct {
	ID           string    `json:"id"`
	DisplayName  string    `json:"displayName"`
	AuthorizedAt time.Time `json:"authorizedAt"`
}
type ReaderSession struct {
	AccessToken          string       `json:"accessToken"`
	AccessTokenExpiresAt time.Time    `json:"accessTokenExpiresAt"`
	RefreshToken         string       `json:"refreshToken"`
	Reader               any          `json:"reader"`
	AccessMode           AccessMode   `json:"accessMode"`
	DeviceAccess         DeviceAccess `json:"deviceAccess"`
}
type AdminSession struct {
	AccessToken          string    `json:"accessToken"`
	AccessTokenExpiresAt time.Time `json:"accessTokenExpiresAt"`
	CSRFToken            string    `json:"csrfToken"`
	Owner                Owner     `json:"owner"`
	Capabilities         []string  `json:"capabilities"`
}

type Principal struct{ AccountID, Role, SessionID string }
type CursorPage[T any] struct {
	Items      []T     `json:"items"`
	NextCursor *string `json:"nextCursor"`
	Total      *int    `json:"total,omitempty"`
}
type ReadingLocator struct {
	Href               string  `json:"href"`
	CFI                *string `json:"cfi,omitempty"`
	Progression        float64 `json:"progression"`
	DisplayedPage      *int    `json:"displayedPage,omitempty"`
	DisplayedPageCount *int    `json:"displayedPageCount,omitempty"`
}
type ReadingProgress struct {
	BookID          string         `json:"bookId"`
	Locator         ReadingLocator `json:"locator"`
	ProgressPercent float64        `json:"progressPercent"`
	UpdatedAt       time.Time      `json:"updatedAt"`
	Version         int            `json:"version"`
}

type PaymentPurpose string
type PaymentProvider interface {
	CreateCheckout(PaymentCreateRequest) (PaymentCheckout, error)
	VerifyCheckout(string, map[string]any) (PaymentCheckout, error)
	Reconcile([]map[string]any) error
	GetProduct(string) (map[string]any, error)
	SyncProduct(string) error
	RefundStatus(string) (string, error)
	Health() string
}
type PaymentCreateRequest struct {
	Purpose          PaymentPurpose
	BookID, DeviceID string
	Amount           Money
}
type PaymentCheckout struct {
	ID           string  `json:"id"`
	Purpose      string  `json:"purpose"`
	Provider     string  `json:"provider"`
	Status       string  `json:"status"`
	QuotedAmount Money   `json:"quotedAmount"`
	ProductID    *string `json:"productId,omitempty"`
}
