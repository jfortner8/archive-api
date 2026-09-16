package storage

import (
	"strings"
	"testing"
)

func TestBuildKey(t *testing.T) {
	key := BuildKey("item-123", "front", "photo.jpg")

	want := "items/item-123/front-photo.jpg"
	if key != want {
		t.Errorf("BuildKey() = %q, want %q", key, want)
	}
}

func TestBuildKeyKeepsFilesUnderSharedItemPrefix(t *testing.T) {
	front := BuildKey("item-123", "front", "photo.jpg")
	back := BuildKey("item-123", "back", "photo.jpg")

	prefix := "items/item-123/"
	if !strings.HasPrefix(front, prefix) || !strings.HasPrefix(back, prefix) {
		t.Errorf("expected both files under %q, got %q and %q", prefix, front, back)
	}
}
