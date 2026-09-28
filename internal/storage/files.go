// Package storage generates presigned S3 URLs so clients can upload and
// download files directly, without the API ever handling file bytes.
package storage

import (
	"context"
	"fmt"
	"mime"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

const (
	// UploadURLExpiry is short: an upload URL is handed out and used
	// immediately.
	UploadURLExpiry = 15 * time.Minute

	// DownloadURLExpiry has to outlive a browsing session, because these are
	// now embedded in every item response rather than fetched one at a time.
	// Too short and a gallery left open goes blank; SigV4 permits up to seven
	// days, and six hours is comfortably longer than anyone looks at one page.
	DownloadURLExpiry = 6 * time.Hour
)

// FileStore issues presigned URLs against a single S3 bucket.
type FileStore struct {
	presign *s3.PresignClient
	bucket  string
}

func NewFileStore(presign *s3.PresignClient, bucket string) *FileStore {
	return &FileStore{presign: presign, bucket: bucket}
}

// ObjectKey generates the S3 key for one file.
//
// The caller's filename is deliberately absent. The previous scheme
// interpolated it raw, which meant a name containing "../" escaped the
// prefix, and two uploads of the same role and filename silently overwrote
// one object while creating two records pointing at it. Keying on the file
// id - minted before the upload - makes both impossible, and the original
// name is kept as an attribute for Content-Disposition instead.
//
// The archive prefix keeps a whole archive deletable as one prefix, and the
// item prefix does the same a level down.
func ObjectKey(archiveID, itemID, fileID, ext string) string {
	if ext != "" && !strings.HasPrefix(ext, ".") {
		ext = "." + ext
	}
	return fmt.Sprintf("originals/%s/%s/%s%s", archiveID, itemID, fileID, ext)
}

// ExtensionFor maps a content type to a file extension for the key. The
// extension is cosmetic - it makes objects recognisable in the console - so
// an unknown type simply gets none.
func ExtensionFor(contentType string) string {
	if i := strings.IndexByte(contentType, ';'); i >= 0 {
		contentType = strings.TrimSpace(contentType[:i])
	}
	switch strings.ToLower(contentType) {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/webp":
		return ".webp"
	case "image/heic":
		return ".heic"
	case "image/tiff":
		return ".tif"
	case "application/pdf":
		return ".pdf"
	case "audio/mpeg":
		return ".mp3"
	case "audio/mp4", "audio/x-m4a":
		return ".m4a"
	case "audio/wav", "audio/x-wav":
		return ".wav"
	case "video/mp4":
		return ".mp4"
	case "video/quicktime":
		return ".mov"
	}
	if exts, err := mime.ExtensionsByType(contentType); err == nil && len(exts) > 0 {
		return exts[0]
	}
	return ""
}

// PresignUpload returns a URL the client can PUT the file bytes to
// directly, plus the key it will end up at.
func (f *FileStore) PresignUpload(ctx context.Context, key, contentType string) (string, error) {
	req, err := f.presign.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:      &f.bucket,
		Key:         &key,
		ContentType: &contentType,
	}, s3.WithPresignExpires(UploadURLExpiry))
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
	}, s3.WithPresignExpires(DownloadURLExpiry))
	if err != nil {
		return "", fmt.Errorf("presign download: %w", err)
	}
	return req.URL, nil
}
