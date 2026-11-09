package media

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math"
	"strings"
	"testing"
)

// drawing builds a photograph-like test image: smooth gradients with a few
// soft bands, so it has something for a resize to interpolate while still
// compressing the way a real photograph does.
//
// An earlier version used an x^y pattern, which is high-entropy noise. JPEG
// cannot compress that at all, so every "derivative" came out larger than its
// original and the tests described a world that does not exist.
func drawing(width, height int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		fy := float64(y) / float64(max(height-1, 1))
		for x := 0; x < width; x++ {
			fx := float64(x) / float64(max(width-1, 1))
			band := 0.15 * math.Sin(fx*6.0) * math.Cos(fy*4.0)
			img.Set(x, y, color.RGBA{
				R: clamp8((fx + band) * 255),
				G: clamp8((fy + band) * 255),
				B: clamp8((0.5 + band) * 255),
				A: 255,
			})
		}
	}
	return img
}

func clamp8(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func encodeJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	return buf.Bytes()
}

func TestDeriveProducesTheLadder(t *testing.T) {
	original := encodeJPEG(t, drawing(2400, 1800))

	dims, variants, err := Derive(bytes.NewReader(original))
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}

	if dims.Width != 2400 || dims.Height != 1800 {
		t.Errorf("dimensions = %dx%d, want 2400x1800", dims.Width, dims.Height)
	}

	if len(variants) == 0 {
		t.Fatal("no variants at all for a 2400px original")
	}

	for _, size := range Ladder() {
		encoded, ok := variants[size.Name]
		if !ok {
			// A rung is dropped when its re-encode would not be smaller than
			// the original, which is a guarantee rather than a failure.
			continue
		}

		img, err := jpeg.Decode(bytes.NewReader(encoded))
		if err != nil {
			t.Errorf("%s does not decode: %v", size.Name, err)
			continue
		}

		b := img.Bounds()
		longest := max(b.Dx(), b.Dy())
		if longest != size.MaxEdge {
			t.Errorf("%s longest edge = %d, want %d", size.Name, longest, size.MaxEdge)
		}

		// Aspect ratio is preserved: 4:3 in, 4:3 out.
		if got, want := float64(b.Dx())/float64(b.Dy()), 2400.0/1800.0; got < want-0.01 || got > want+0.01 {
			t.Errorf("%s aspect = %.3f, want %.3f", size.Name, got, want)
		}

		// The whole point: each rung is materially smaller than the original.
		if len(encoded) >= len(original) {
			t.Errorf("%s is %d bytes against an original of %d - no saving", size.Name, len(encoded), len(original))
		}
	}
}

// TestDeriveDoesNotUpscale: enlarging a small scan makes a bigger file that
// looks worse, so rungs above the original are skipped.
func TestDeriveDoesNotUpscale(t *testing.T) {
	original := encodeJPEG(t, drawing(500, 500))

	_, variants, err := Derive(bytes.NewReader(original))
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}

	if _, ok := variants["card"]; ok {
		t.Error("a 500px original produced an 800px card variant")
	}
	if _, ok := variants["full"]; ok {
		t.Error("a 500px original produced a 1600px full variant")
	}

	// The thumb is still generated: a cheap small JPEG beats the client
	// scaling the original, whatever format that was in.
	thumb, ok := variants["thumb"]
	if !ok {
		t.Fatal("no thumb for a small original")
	}
	img, err := jpeg.Decode(bytes.NewReader(thumb))
	if err != nil {
		t.Fatalf("thumb does not decode: %v", err)
	}
	if got := max(img.Bounds().Dx(), img.Bounds().Dy()); got != 400 {
		t.Errorf("thumb longest edge = %d, want 400", got)
	}
}

func TestDeriveHandlesPortraitAndPNG(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, drawing(900, 2700)); err != nil {
		t.Fatalf("encode png: %v", err)
	}

	dims, variants, err := Derive(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("Derive: %v", err)
	}
	if dims.Width != 900 || dims.Height != 2700 {
		t.Errorf("dimensions = %dx%d, want 900x2700", dims.Width, dims.Height)
	}

	img, err := jpeg.Decode(bytes.NewReader(variants["thumb"]))
	if err != nil {
		t.Fatalf("thumb does not decode: %v", err)
	}
	// The long edge is the height here, so that is what gets capped.
	if img.Bounds().Dy() != 400 {
		t.Errorf("portrait thumb height = %d, want 400", img.Bounds().Dy())
	}
	if img.Bounds().Dx() >= img.Bounds().Dy() {
		t.Error("a portrait original produced a landscape thumb")
	}
}

// TestHEICIsNamedRatherThanGuessedAt covers the format that will actually
// turn up, since iPhones default to it.
func TestHEICIsNamedRatherThanGuessedAt(t *testing.T) {
	// A minimal ISO base-media header with the heic brand. Not decodable -
	// the point is that the failure explains itself.
	heic := append([]byte{0, 0, 0, 24}, []byte("ftypheic")...)
	heic = append(heic, make([]byte, 64)...)

	_, _, err := Derive(bytes.NewReader(heic))
	if err == nil {
		t.Fatal("a HEIC file was accepted")
	}

	var unsupported *ErrUnsupportedFormat
	if !asUnsupported(err, &unsupported) {
		t.Fatalf("error = %T, want *ErrUnsupportedFormat", err)
	}
	if !strings.Contains(unsupported.Reason, "HEIC") {
		t.Errorf("reason = %q, want it to name HEIC", unsupported.Reason)
	}
	if !strings.Contains(unsupported.Reason, "original is kept") {
		t.Errorf("reason = %q, want it to say the original still works", unsupported.Reason)
	}
}

func TestDeriveRejectsNonImages(t *testing.T) {
	_, _, err := Derive(strings.NewReader("this is not an image at all"))
	if err == nil {
		t.Fatal("arbitrary bytes were accepted as an image")
	}
	var unsupported *ErrUnsupportedFormat
	if !asUnsupported(err, &unsupported) {
		t.Errorf("error = %T, want *ErrUnsupportedFormat", err)
	}
}

func TestParseOriginalKey(t *testing.T) {
	tests := []struct {
		key                 string
		archive, item, file string
		wantErr             bool
	}{
		{
			key:     "originals/arc_1/itm_2/fil_3.jpg",
			archive: "arc_1", item: "itm_2", file: "fil_3",
		},
		{
			// An unrecognised content type leaves no extension.
			key:     "originals/arc_1/itm_2/fil_3",
			archive: "arc_1", item: "itm_2", file: "fil_3",
		},
		{key: "derived/arc_1/itm_2/fil_3/thumb.jpg", wantErr: true},
		{key: "originals/arc_1/itm_2", wantErr: true},
		{key: "originals//itm_2/fil_3.jpg", wantErr: true},
		{key: "nonsense", wantErr: true},
	}

	for _, tt := range tests {
		archive, item, file, err := ParseOriginalKey(tt.key)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseOriginalKey(%q) succeeded, want an error", tt.key)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseOriginalKey(%q): %v", tt.key, err)
			continue
		}
		if archive != tt.archive || item != tt.item || file != tt.file {
			t.Errorf("ParseOriginalKey(%q) = %q/%q/%q, want %q/%q/%q",
				tt.key, archive, item, file, tt.archive, tt.item, tt.file)
		}
	}
}

// TestDerivedKeysAreSeparableFromOriginals matters for the lifecycle rules:
// originals are cold and archivable, derivatives are hot and must stay in
// Standard, so a prefix has to tell them apart.
func TestDerivedKeysAreSeparableFromOriginals(t *testing.T) {
	derived := DerivedKey("arc_1", "itm_2", "fil_3", "thumb")

	if !strings.HasPrefix(derived, "derived/") {
		t.Errorf("derived key = %q, not under derived/", derived)
	}
	if _, _, _, err := ParseOriginalKey(derived); err == nil {
		t.Error("a derived key parsed as an original, which would re-trigger derivation on itself")
	}
}

func asUnsupported(err error, target **ErrUnsupportedFormat) bool {
	if u, ok := err.(*ErrUnsupportedFormat); ok {
		*target = u
		return true
	}
	return false
}
