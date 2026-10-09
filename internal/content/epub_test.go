package content

import (
	"archive/zip"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"strings"
	"testing"
)

func testEPUB(t *testing.T, chapter string) []byte {
	t.Helper()
	var out bytes.Buffer
	writer := zip.NewWriter(&out)
	header := &zip.FileHeader{Name: "mimetype", Method: zip.Store}
	entry, _ := writer.CreateHeader(header)
	_, _ = entry.Write([]byte("application/epub+zip"))
	files := map[string]string{"META-INF/container.xml": `<?xml version="1.0"?><container><rootfiles><rootfile full-path="OEBPS/content.opf"/></rootfiles></container>`, "OEBPS/content.opf": `<?xml version="1.0"?><package><manifest><item id="chapter" href="chapter.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="chapter"/></spine></package>`, "OEBPS/chapter.xhtml": chapter}
	for name, data := range files {
		entry, _ = writer.Create(name)
		_, _ = entry.Write([]byte(data))
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}
func TestProcessEPUBSanitizesScripts(t *testing.T) {
	epub, err := ProcessEPUB(testEPUB(t, `<html><body onload="bad()"><script>bad()</script><p>Hello</p></body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	if len(epub.Resources) != 1 {
		t.Fatalf("resources=%d", len(epub.Resources))
	}
	if bytes.Contains(bytes.ToLower(epub.Resources[0].Data), []byte("script")) || bytes.Contains(bytes.ToLower(epub.Resources[0].Data), []byte("onload")) {
		t.Fatal("active content survived sanitization")
	}
}
func TestProcessEPUBRejectsRemoteResources(t *testing.T) {
	_, err := ProcessEPUB(testEPUB(t, `<html><body><img src="https://example.com/a.jpg"/></body></html>`))
	if err == nil {
		t.Fatal("expected remote resource rejection")
	}
}
func TestProcessEPUBRemovesRemoteAnchorWithoutRejectingBook(t *testing.T) {
	epub, err := ProcessEPUB(testEPUB(t, `<html><body><a href="https://example.com/publisher">Publisher</a></body></html>`))
	if err != nil {
		t.Fatalf("remote hyperlink should be sanitized, not reject the EPUB: %v", err)
	}
	chapter := strings.ToLower(string(epub.Resources[0].Data))
	if strings.Contains(chapter, "https://example.com") {
		t.Fatal("remote hyperlink survived sanitization")
	}
	if !strings.Contains(chapter, "publisher") {
		t.Fatal("anchor text was removed with its remote destination")
	}
}
func TestProcessEPUBAllowsStandardXHTMLNamespace(t *testing.T) {
	_, err := ProcessEPUB(testEPUB(t, `<html xmlns="http://www.w3.org/1999/xhtml"><head><title>Book</title></head><body><p>Hello</p></body></html>`))
	if err != nil {
		t.Fatalf("standard XHTML namespace was rejected: %v", err)
	}
}
func TestPackageEncryptionRoundTrip(t *testing.T) {
	plain := []byte("book")
	ciphertext, key, nonce, _, err := EncryptPackage(plain)
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := WrapKey(bytes.Repeat([]byte{7}, 32), key)
	if err != nil {
		t.Fatal(err)
	}
	unwrapped, err := UnwrapKey(bytes.Repeat([]byte{7}, 32), wrapped)
	if err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(unwrapped)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil || !bytes.Equal(decoded, plain) {
		t.Fatal("round trip failed")
	}
}
