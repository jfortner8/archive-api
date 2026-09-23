package storage

import (
	"strings"
	"testing"
)

// TestObjectKeyIgnoresTheCallersFilename is the regression test for the key
// scheme this replaced, which interpolated the caller's filename raw:
//
//	accounts/%s/items/%s/%s-%s   (account, item, role, filename)
//
// A name containing "../" escaped the prefix, and two uploads of the same
// role and filename silently overwrote one object while creating two records
// pointing at it. Keying on the file id removes both possibilities.
func TestObjectKeyIgnoresTheCallersFilename(t *testing.T) {
	key := ObjectKey("arc_1", "itm_2", "fil_3", ExtensionFor("image/jpeg"))

	if want := "originals/arc_1/itm_2/fil_3.jpg"; key != want {
		t.Errorf("key = %q, want %q", key, want)
	}

	// Two uploads differing only in filename get different keys, because the
	// filename is not in the key at all.
	a := ObjectKey("arc_1", "itm_2", "fil_a", ExtensionFor("image/jpeg"))
	b := ObjectKey("arc_1", "itm_2", "fil_b", ExtensionFor("image/jpeg"))
	if a == b {
		t.Error("two files collided on one key")
	}
}

func TestObjectKeyPrefixesAreNestable(t *testing.T) {
	key := ObjectKey("arc_1", "itm_2", "fil_3", ".jpg")

	// An archive's objects, and then an item's, are each deletable as one
	// prefix rather than by walking records.
	if !strings.HasPrefix(key, "originals/arc_1/") {
		t.Errorf("key = %q, not under the archive prefix", key)
	}
	if !strings.HasPrefix(key, "originals/arc_1/itm_2/") {
		t.Errorf("key = %q, not under the item prefix", key)
	}

	// Different archives never share a prefix, so one archive's prefix delete
	// can never reach another's objects.
	other := ObjectKey("arc_9", "itm_2", "fil_3", ".jpg")
	if strings.HasPrefix(other, "originals/arc_1/") {
		t.Errorf("archive arc_9 produced a key under arc_1: %q", other)
	}
}

func TestExtensionFor(t *testing.T) {
	tests := []struct {
		contentType string
		want        string
	}{
		{contentType: "image/jpeg", want: ".jpg"},
		{contentType: "image/heic", want: ".heic"},
		{contentType: "application/pdf", want: ".pdf"},
		{contentType: "application/pdf; version=1.7", want: ".pdf"},
		{contentType: "IMAGE/PNG", want: ".png"},
		{contentType: "audio/mp4", want: ".m4a"},
		// The extension is cosmetic, so something unrecognised simply gets
		// none rather than failing an upload.
		{contentType: "application/x-nonsense", want: ""},
	}

	for _, tt := range tests {
		if got := ExtensionFor(tt.contentType); got != tt.want {
			t.Errorf("ExtensionFor(%q) = %q, want %q", tt.contentType, got, tt.want)
		}
	}
}

// TestObjectKeyHandlesAMissingExtension covers the unrecognised-type path
// end to end: a key with no extension is still a valid, unique key.
func TestObjectKeyHandlesAMissingExtension(t *testing.T) {
	key := ObjectKey("arc_1", "itm_2", "fil_3", ExtensionFor("application/x-nonsense"))
	if want := "originals/arc_1/itm_2/fil_3"; key != want {
		t.Errorf("key = %q, want %q", key, want)
	}
}
