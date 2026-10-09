package objectstore

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestPresignPartDoesNotReturnBrowserForbiddenHostHeader(t *testing.T) {
	cfg := aws.Config{Region: "auto", Credentials: credentials.NewStaticCredentialsProvider("test", "test", "")}
	client := s3.NewFromConfig(cfg, func(options *s3.Options) {
		options.BaseEndpoint = aws.String("http://localhost:9000")
		options.UsePathStyle = true
	})
	store := &Store{presign: s3.NewPresignClient(client)}

	signedURL, headers, err := store.PresignPart(context.Background(), "bucket", "book.epub", "upload-id", 1)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(signedURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		t.Fatalf("PresignPart URL = %q, want an absolute URL", signedURL)
	}
	for name := range headers {
		if strings.EqualFold(name, "host") {
			t.Fatal("Host is signed automatically by the browser and must not be returned as a JavaScript-settable header")
		}
	}
}
