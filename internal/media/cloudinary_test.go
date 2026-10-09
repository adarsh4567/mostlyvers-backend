package media

import "testing"

func TestReferenceRoundTrip(t *testing.T) {
	reference := prefix + "mostlyvers/book-covers/example|https://res.cloudinary.com/demo/image/upload/example.webp"
	if !IsReference(reference) {
		t.Fatal("expected Cloudinary reference")
	}
	if got := URL(reference); got != "https://res.cloudinary.com/demo/image/upload/example.webp" {
		t.Fatalf("URL() = %q", got)
	}
	if URL("book-covers/example") != "" {
		t.Fatal("non-Cloudinary object key must not expose a Cloudinary URL")
	}
}
