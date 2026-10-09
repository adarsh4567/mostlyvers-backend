package payment

import (
	"github.com/mostlyvers/backend/internal/domain"
	"github.com/mostlyvers/backend/internal/httpx"
	"net/http"
)

type Disabled struct{}

func unavailable() error {
	return httpx.NewError(http.StatusServiceUnavailable, "PAYMENT_NOT_CONFIGURED", "Payments are not configured yet.")
}
func (Disabled) CreateCheckout(domain.PaymentCreateRequest) (domain.PaymentCheckout, error) {
	return domain.PaymentCheckout{}, unavailable()
}
func (Disabled) VerifyCheckout(string, map[string]any) (domain.PaymentCheckout, error) {
	return domain.PaymentCheckout{}, unavailable()
}
func (Disabled) Reconcile([]map[string]any) error          { return unavailable() }
func (Disabled) GetProduct(string) (map[string]any, error) { return nil, unavailable() }
func (Disabled) SyncProduct(string) error                  { return unavailable() }
func (Disabled) RefundStatus(string) (string, error)       { return "", unavailable() }
func (Disabled) Health() string                            { return "NOT_CONFIGURED" }
