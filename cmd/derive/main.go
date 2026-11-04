// Command derive generates thumbnails when a file lands in S3.
//
// It is triggered by an ObjectCreated notification on the originals/ prefix
// rather than called by the API, which keeps a 20-megapixel scan from being
// resized inside a request someone is waiting on. The notification is also
// the only commit signal that matters: a file is real once its bytes are in
// S3, whatever the client did or did not get around to confirming.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/aws/aws-lambda-go/events"
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/jfortner8/archive-api/internal/awsclients"
	"github.com/jfortner8/archive-api/internal/config"
	"github.com/jfortner8/archive-api/internal/domain"
	"github.com/jfortner8/archive-api/internal/domain/itemtypes"
	"github.com/jfortner8/archive-api/internal/domain/subjecttypes"
	"github.com/jfortner8/archive-api/internal/media"
	"github.com/jfortner8/archive-api/internal/store"
)

type deriver struct {
	s3     *s3.Client
	store  *store.Store
	bucket string
}

func main() {
	ctx := context.Background()

	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "error", err)
		os.Exit(1)
	}

	clients, err := awsclients.New(ctx, cfg)
	if err != nil {
		slog.Error("aws clients", "error", err)
		os.Exit(1)
	}

	d := &deriver{
		s3:     clients.S3,
		store:  store.New(clients.Dynamo, cfg.DynamoTable, itemtypes.Default, subjecttypes.Default),
		bucket: cfg.S3Bucket,
	}

	lambda.Start(d.handle)
}

func (d *deriver) handle(ctx context.Context, event events.S3Event) error {
	for _, record := range event.Records {
		key := record.S3.Object.URLDecodedKey
		if key == "" {
			key = record.S3.Object.Key
		}

		// The notification is scoped to originals/ in Terraform, but checking
		// again here is what guarantees the function cannot be made to
		// consume its own output - a derivative landing back on this handler
		// would recurse without bound.
		if !strings.HasPrefix(key, "originals/") {
			slog.Info("ignoring an object outside originals/", "key", key)
			continue
		}

		if err := d.deriveOne(ctx, key); err != nil {
			// One bad object must not fail the batch and have S3 redeliver
			// every sibling with it. The record is marked failed, which is
			// what the client renders a placeholder from.
			slog.Error("deriving", "error", err, "key", key)
		}
	}
	return nil
}

func (d *deriver) deriveOne(ctx context.Context, key string) error {
	archiveID, itemID, fileID, err := media.ParseOriginalKey(key)
	if err != nil {
		return err
	}

	archive := domain.ArchiveID(archiveID)
	item := domain.ItemID(itemID)
	file := domain.FileID(fileID)

	original, err := d.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: &d.bucket,
		Key:    &key,
	})
	if err != nil {
		return fmt.Errorf("get original: %w", err)
	}
	defer original.Body.Close()

	dims, rendered, err := media.Derive(original.Body)
	if err != nil {
		var unsupported *media.ErrUnsupportedFormat
		if errors.As(err, &unsupported) {
			// Not a failure of this item so much as of this format. The
			// original stays downloadable and the gallery serves it
			// full-size; marking it failed is how the client knows no
			// thumbnail is ever coming, as opposed to not yet.
			slog.Warn("no derivatives for this format",
				"key", key, "reason", unsupported.Reason)
			_, markErr := d.store.SetFileDerivatives(
				ctx, archive, item, file, 0, 0, nil, domain.FileStatusFailed)
			return markErr
		}
		return err
	}

	variants := make([]domain.FileVariant, 0, len(rendered))
	for _, size := range media.Ladder() {
		encoded, ok := rendered[size.Name]
		if !ok {
			continue
		}

		variantKey := media.DerivedKey(archiveID, itemID, fileID, size.Name)
		if _, err := d.s3.PutObject(ctx, &s3.PutObjectInput{
			Bucket:      &d.bucket,
			Key:         &variantKey,
			Body:        bytes.NewReader(encoded),
			ContentType: aws.String("image/jpeg"),
			// Derivatives are immutable: a replaced file gets a new file id
			// and therefore a new key, so nothing ever overwrites one.
			CacheControl: aws.String("public, max-age=31536000, immutable"),
		}); err != nil {
			return fmt.Errorf("put %s variant: %w", size.Name, err)
		}

		width, height := variantDimensions(dims, size.MaxEdge)
		variants = append(variants, domain.FileVariant{
			Name:   size.Name,
			Key:    variantKey,
			Width:  width,
			Height: height,
			Bytes:  int64(len(encoded)),
		})
	}

	if _, err := d.store.SetFileDerivatives(
		ctx, archive, item, file, dims.Width, dims.Height, variants, domain.FileStatusReady,
	); err != nil {
		// The objects are already in S3. Leaving them is harmless - they are
		// unreferenced and a lifecycle rule will not miss them - whereas
		// failing loudly here gets the notification redelivered, which is
		// what we want: the write is the part that matters.
		return fmt.Errorf("record derivatives: %w", err)
	}

	slog.Info("derived",
		"key", key,
		"original", fmt.Sprintf("%dx%d", dims.Width, dims.Height),
		"variants", len(variants),
	)
	return nil
}

// variantDimensions recomputes a rung's size from the original's, which is
// cheaper than decoding the JPEG that was just encoded and gives the same
// answer because both use the same fit.
func variantDimensions(dims media.Dimensions, maxEdge int) (int, int) {
	return media.Fit(dims, maxEdge)
}
