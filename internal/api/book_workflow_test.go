package api

import (
	"errors"
	"testing"
	"time"
)

func TestSafeContentErrorCodeDoesNotExposeResourceNames(t *testing.T) {
	if got := safeContentErrorCode(errors.New("EPUB_RESOURCE_MISSING: private/chapter-name.xhtml")); got != "EPUB_RESOURCE_MISSING" {
		t.Fatalf("error code = %q", got)
	}
	if got := safeContentErrorCode(errors.New("unexpected parser detail")); got != "EPUB_PROCESSING_FAILED" {
		t.Fatalf("unexpected error code = %q", got)
	}
}

func TestNormalizeCreateBookInputSuppliesDraftFreeDefaults(t *testing.T) {
	now := time.Date(2026, time.October, 10, 12, 0, 0, 0, time.UTC)
	input, err := normalizeCreateBookInput(createBookInput{
		Title:            "  The Quiet River  ",
		ContentUploadRef: "upload-1",
	}, now, 15)
	if err != nil {
		t.Fatal(err)
	}
	if input.Title != "The Quiet River" || input.Status != "DRAFT" {
		t.Fatalf("unexpected identity defaults: %#v", input)
	}
	if input.Price.AmountMinor != 0 || input.Price.Currency != "INR" {
		t.Fatalf("new free-launch book price = %#v, want INR 0", input.Price)
	}
	if input.PublicationMonth != 10 || input.PublicationYear != 2026 {
		t.Fatalf("publication = %d/%d, want 10/2026", input.PublicationMonth, input.PublicationYear)
	}
	if input.Prebook.Enabled || input.Prebook.DiscountPercent != 15 {
		t.Fatalf("unexpected prebook defaults: %#v", input.Prebook)
	}
}

func TestNormalizeCreateBookInputRequiresTitleAndEPUB(t *testing.T) {
	for name, input := range map[string]createBookInput{
		"title": {ContentUploadRef: "upload-1"},
		"epub":  {Title: "A title"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := normalizeCreateBookInput(input, time.Now(), 15); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestAdminBookActionsReflectLifecycleAndRetention(t *testing.T) {
	ready := buildAdminBookActions("DRAFT", "VALID", false)
	if !ready.CanEdit || !ready.CanPublish || ready.CanArchive || !ready.CanDelete {
		t.Fatalf("unexpected ready draft actions: %#v", ready)
	}

	processing := buildAdminBookActions("DRAFT", "PROCESSING", false)
	if processing.CanPublish || processing.PublishBlockedReason == "" || processing.CanDelete || processing.DeleteBlockedReason == "" {
		t.Fatalf("processing draft must explain why publish is unavailable: %#v", processing)
	}

	usedArchive := buildAdminBookActions("ARCHIVED", "VALID", true)
	if !usedArchive.CanEdit || usedArchive.CanDelete || usedArchive.DeleteBlockedReason == "" {
		t.Fatalf("used archive must be retained with an explanation: %#v", usedArchive)
	}
}

func TestFreeLaunchBookPriceDoesNotChangeHistoricalRevenue(t *testing.T) {
	price := presentedBookPrice("FREE_LAUNCH", 13500, "INR")
	if price.AmountMinor != 0 || price.Currency != "INR" {
		t.Fatalf("catalog price = %#v, want free", price)
	}
	item := buildTopBook("book-1", "Example", "", 1, price.AmountMinor, 13500, "INR")
	if item.Revenue.AmountMinor != 13500 {
		t.Fatalf("historical revenue = %d, want 13500", item.Revenue.AmountMinor)
	}
}
