package objectstore

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/mostlyvers/backend/internal/config"
)

type Store struct {
	client                                     *s3.Client
	presign                                    *s3.PresignClient
	PrivateBucket, PublicBucket, PublicBaseURL string
}

func New(ctx context.Context, cfg config.Config) (*Store, error) {
	options := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(cfg.R2Region), awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.R2AccessKey, cfg.R2SecretKey, ""))}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, options...)
	if err != nil {
		return nil, err
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if cfg.R2Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.R2Endpoint)
			o.UsePathStyle = true
		}
	})
	presignClient := client
	if cfg.R2PresignEndpoint != "" && cfg.R2PresignEndpoint != cfg.R2Endpoint {
		presignClient = s3.NewFromConfig(awsCfg, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(cfg.R2PresignEndpoint)
			o.UsePathStyle = true
		})
	}
	return &Store{client: client, presign: s3.NewPresignClient(presignClient), PrivateBucket: cfg.R2Bucket, PublicBucket: cfg.R2PublicBucket, PublicBaseURL: cfg.R2PublicBaseURL}, nil
}
func (s *Store) CreateMultipart(ctx context.Context, bucket, key, contentType string) (string, error) {
	result, err := s.client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String(key), ContentType: aws.String(contentType)})
	if err != nil {
		return "", err
	}
	return aws.ToString(result.UploadId), nil
}
func (s *Store) PresignPart(ctx context.Context, bucket, key, uploadID string, part int32) (string, map[string]string, error) {
	result, err := s.presign.PresignUploadPart(ctx, &s3.UploadPartInput{Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID), PartNumber: aws.Int32(part)}, s3.WithPresignExpires(5*time.Minute))
	if err != nil {
		return "", nil, err
	}
	headers := make(map[string]string, len(result.SignedHeader))
	for name, values := range result.SignedHeader {
		// The browser supplies Host from the signed URL. JavaScript is forbidden
		// from setting it explicitly, so returning it would abort XMLHttpRequest.
		if strings.EqualFold(name, "host") {
			continue
		}
		if len(values) > 0 {
			headers[name] = values[0]
		}
	}
	return result.URL, headers, nil
}
func (s *Store) CompleteMultipart(ctx context.Context, bucket, key, uploadID string, parts []types.CompletedPart) error {
	_, err := s.client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID), MultipartUpload: &types.CompletedMultipartUpload{Parts: parts}})
	return err
}
func (s *Store) AbortMultipart(ctx context.Context, bucket, key, uploadID string) error {
	_, err := s.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID)})
	return err
}
func (s *Store) Head(ctx context.Context, bucket, key string) (int64, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return 0, err
	}
	return aws.ToInt64(out.ContentLength), nil
}
func (s *Store) Get(ctx context.Context, bucket, key string, rangeHeader *string) (io.ReadCloser, int64, string, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), Range: rangeHeader})
	if err != nil {
		return nil, 0, "", err
	}
	return out.Body, aws.ToInt64(out.ContentLength), aws.ToString(out.ContentType), nil
}
func (s *Store) Put(ctx context.Context, bucket, key, contentType string, body io.Reader, length int64) error {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(key), ContentType: aws.String(contentType), Body: body, ContentLength: aws.Int64(length)})
	return err
}
func (s *Store) Delete(ctx context.Context, bucket, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	return err
}
func (s *Store) Usage(ctx context.Context) (int64, error) {
	var total int64
	for _, bucket := range []string{s.PrivateBucket, s.PublicBucket} {
		paginator := s3.NewListObjectsV2Paginator(s.client, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
		for paginator.HasMorePages() {
			page, err := paginator.NextPage(ctx)
			if err != nil {
				return 0, err
			}
			for _, item := range page.Contents {
				total += aws.ToInt64(item.Size)
			}
		}
	}
	return total, nil
}
func (s *Store) PresignGet(ctx context.Context, bucket, key string) (string, error) {
	result, err := s.presign.PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)}, s3.WithPresignExpires(2*time.Minute))
	if err != nil {
		return "", err
	}
	return result.URL, nil
}
func (s *Store) PublicURL(key string) string {
	if s.PublicBaseURL == "" {
		return ""
	}
	return fmt.Sprintf("%s/%s", s.PublicBaseURL, key)
}
