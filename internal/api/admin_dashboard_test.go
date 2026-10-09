package api

import "testing"

func TestBuildTopBookIncludesCatalogPriceAndRevenue(t *testing.T) {
	item := buildTopBook("book-1", "Example", "/cover.png", 0, 13500, 0, "INR")
	if item.Price.AmountMinor != 13500 {
		t.Fatalf("price = %d, want 13500", item.Price.AmountMinor)
	}
	if item.Revenue.AmountMinor != 0 {
		t.Fatalf("revenue = %d, want 0", item.Revenue.AmountMinor)
	}
}
