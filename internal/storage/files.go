// Package storage generates presigned S3 URLs so clients can upload and
// download files directly, without the API ever handling file bytes.
package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const (
	uploadURLExpiry   = 15 * time.Minute
	downloadURLExpiry = 1 * time.Hour
)

// FileStore issues presigned URLs against a single S3 bucket.
type FileStore struct {
	presign *s3.PresignClient
	bucket  string
}

func NewFileStore(presign *s3.PresignClient, bucket string) *FileStore {
	return &FileStore{presign: presign, bucket: bucket}
}

// BuildKey generates the S3 object key for a file belonging to an item.
// Keeping all of an item's files under a shared prefix makes it easy to
// find/delete them together later; prefixing that by account does the
// same one level up, so deleting an entire account's data is a single S3
// prefix delete instead of a per-item walk.
func BuildKey(accountID, itemID, role, filename string) string {
	return fmt.Sprintf("accounts/%s/items/%s/%s-%s", accountID, itemID, role, filename)
}

// PresignUpload returns a URL the client can PUT the file bytes to
// directly, plus the key it will end up at.
func (f *FileStore) PresignUpload(ctx context.Context, key, contentType string) (string, error) {
	req, err := f.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:      &f.bucket,
		Key:         &key,
		ContentType: &contentType,
	}, s3.WithPresignExpires(uploadURLExpiry))
	if err != nil {
		return "", fmt.Errorf("presign upload: %w", err)
	}
	return req.URL, nil
}

// PresignDownload returns a temporary URL the client can GET the file from.
func (f *FileStore) PresignDownload(ctx context.Context, key string) (string, error) {
	req, err := f.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: &f.bucket,
		Key:    &key,
	}, s3.WithPresignExpires(downloadURLExpiry))
	if err != nil {
		return "", fmt.Errorf("presign download: %w", err)
	}
	return req.URL, nil
}
