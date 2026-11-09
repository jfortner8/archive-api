// Package media turns an uploaded original into the sizes the UI actually
// renders.
//
// A gallery tile is around 150 pixels and a timeline thumbnail 112; serving a
// 1200-pixel scan into either wastes most of what it downloads. These
// derivatives are what stop that, and they also carry the intrinsic
// dimensions that let a layout reserve space instead of reflowing once the
// image arrives.
package media

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"strings"

	// Decoders register themselves on import. JPEG, PNG and GIF come from the
	// standard library; TIFF and WebP (decode only) from x/image, both of
	// which turn up in scanned archives.
	_ "image/gif"
	_ "image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/tiff"
	_ "golang.org/x/image/webp"
)

// Size is one rung of the ladder.
type Size struct {
	Name    string
	MaxEdge int
	Quality int
}

// Ladder is the set generated for every image.
//
// Three rungs, sized to what the UI draws: thumb for a timeline strip, card
// for a gallery tile at up to 2x pixel density, full for the lightbox. A
// fourth would cost storage and generation time for a size nothing requests.
func Ladder() []Size {
	return []Size{
		{Name: "thumb", MaxEdge: 400, Quality: 80},
		{Name: "card", MaxEdge: 800, Quality: 82},
		{Name: "full", MaxEdge: 1600, Quality: 85},
	}
}

// Dimensions is the original's intrinsic size.
type Dimensions struct {
	Width  int
	Height int
}

// ErrUnsupportedFormat means nothing here can decode those bytes.
//
// The notable case is HEIC, which iPhones produce by default and which has no
// pure-Go decoder - it needs libvips or similar, which in turn needs a
// container image rather than a zip. Until then a HEIC upload keeps its
// original and simply has no derivatives: the file is stored and downloadable,
// the gallery just serves the full-size version.
type ErrUnsupportedFormat struct {
	Reason string
}

func (e *ErrUnsupportedFormat) Error() string {
	return "unsupported image format: " + e.Reason
}

// Derive decodes an original and renders each rung of the ladder.
//
// Rungs larger than the original are skipped rather than upscaled: enlarging
// a small scan produces a bigger file that looks worse, and the client can
// fall back to a smaller rung.
func Derive(original io.Reader) (Dimensions, map[string][]byte, error) {
	raw, err := io.ReadAll(original)
	if err != nil {
		return Dimensions{}, nil, fmt.Errorf("read original: %w", err)
	}

	src, format, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return Dimensions{}, nil, &ErrUnsupportedFormat{Reason: describeDecodeFailure(raw, err)}
	}

	bounds := src.Bounds()
	dims := Dimensions{Width: bounds.Dx(), Height: bounds.Dy()}
	if dims.Width == 0 || dims.Height == 0 {
		return dims, nil, &ErrUnsupportedFormat{Reason: "decoded to zero pixels"}
	}
	_ = format

	out := map[string][]byte{}
	for _, size := range Ladder() {
		longest := dims.Width
		if dims.Height > longest {
			longest = dims.Height
		}
		if longest <= size.MaxEdge && size.Name != "thumb" {
			// Already smaller than this rung; a larger rung would be an
			// upscale. The thumb is still generated, because a 300px original
			// still wants a cheap 300px JPEG rather than the client scaling a
			// PNG or TIFF.
			continue
		}

		encoded, err := resizeJPEG(src, dims, size)
		if err != nil {
			return dims, nil, err
		}

		// A derivative that is not smaller than the original is not a
		// derivative worth having - serving it would cost more bytes than
		// serving the thing it was made from. This happens for real: a
		// quality-85 re-encode of a quality-75 original, or any image whose
		// detail does not survive JPEG well. Dropping it means every stored
		// variant is guaranteed a saving, and the client falls back to a
		// smaller rung or the original.
		if len(encoded) >= len(raw) {
			continue
		}
		out[size.Name] = encoded
	}

	return dims, out, nil
}

func resizeJPEG(src image.Image, dims Dimensions, size Size) ([]byte, error) {
	width, height := Fit(dims, size.MaxEdge)

	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	// CatmullRom over ApproxBiLinear: downscaling a detailed scan by 4x shows
	// visible aliasing with a bilinear filter, and these are generated once.
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: size.Quality}); err != nil {
		return nil, fmt.Errorf("encode %s: %w", size.Name, err)
	}
	return buf.Bytes(), nil
}

// Fit scales dimensions so the longest edge is at most maxEdge, preserving
// aspect ratio and never returning a zero dimension.
//
// Exported so a caller can size a rung without decoding the JPEG it just
// encoded; both paths use this, so they cannot disagree.
func Fit(dims Dimensions, maxEdge int) (int, int) {
	width, height := dims.Width, dims.Height
	longest := width
	if height > longest {
		longest = height
	}
	if longest <= maxEdge {
		return width, height
	}

	ratio := float64(maxEdge) / float64(longest)
	width = int(float64(width) * ratio)
	height = int(float64(height) * ratio)
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	return width, height
}

// describeDecodeFailure turns Go's terse decode error into something a log
// reader can act on, naming HEIC specifically because it is the one that will
// actually come up.
func describeDecodeFailure(raw []byte, err error) string {
	if looksLikeHEIC(raw) {
		return "HEIC, which has no pure-Go decoder; the original is kept and served full-size"
	}
	if strings.Contains(err.Error(), "unknown format") {
		return "no registered decoder recognised these bytes"
	}
	return err.Error()
}

// looksLikeHEIC checks the ISO base-media brand, since the extension and the
// content type are both caller-supplied and often wrong.
func looksLikeHEIC(raw []byte) bool {
	if len(raw) < 12 {
		return false
	}
	if string(raw[4:8]) != "ftyp" {
		return false
	}
	switch string(raw[8:12]) {
	case "heic", "heix", "hevc", "heim", "heis", "mif1", "msf1":
		return true
	}
	return false
}

// DerivedKey is where a variant is stored.
//
// Parallel to the original's prefix so an item's whole footprint - originals
// and derivatives - is two prefixes rather than a walk, and so a lifecycle
// rule can treat them differently: originals are cold and archivable,
// derivatives are hot and must stay in Standard.
func DerivedKey(archiveID, itemID, fileID, variant string) string {
	return fmt.Sprintf("derived/%s/%s/%s/%s.jpg", archiveID, itemID, fileID, variant)
}

// ParseOriginalKey recovers the ids from an original's key, which is how the
// derivative Lambda knows which record to update from an S3 event alone.
func ParseOriginalKey(key string) (archiveID, itemID, fileID string, err error) {
	rest, ok := strings.CutPrefix(key, "originals/")
	if !ok {
		return "", "", "", fmt.Errorf("key %q is not an original", key)
	}

	parts := strings.Split(rest, "/")
	if len(parts) != 3 {
		return "", "", "", fmt.Errorf("key %q does not have the expected archive/item/file shape", key)
	}

	fileWithExt := parts[2]
	if dot := strings.LastIndexByte(fileWithExt, '.'); dot > 0 {
		fileWithExt = fileWithExt[:dot]
	}
	if parts[0] == "" || parts[1] == "" || fileWithExt == "" {
		return "", "", "", fmt.Errorf("key %q has an empty path segment", key)
	}

	return parts[0], parts[1], fileWithExt, nil
}
