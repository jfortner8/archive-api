package storage

import (
	"strings"
	"testing"
)

func TestBuildKey(t *testing.T) {
	key := BuildKey("account-1", "item-123", "front", "photo.jpg")

	want := "accounts/account-1/items/item-123/front-photo.jpg"
	if key != want {
		t.Errorf("BuildKey() = %q, want %q", key, want)
	}
}

func TestBuildKeyKeepsFilesUnderSharedItemPrefix(t *testing.T) {
	front := BuildKey("account-1", "item-123", "front", "photo.jpg")
	back := BuildKey("account-1", "item-123", "back", "photo.jpg")

	prefix := "accounts/account-1/items/item-123/"
	if !strings.HasPrefix(front, prefix) || !strings.HasPrefix(back, prefix) {
		t.Errorf("expected both files under %q, got %q and %q", prefix, front, back)
	}
}

func TestBuildKeyKeepsItemsUnderSharedAccountPrefix(t *testing.T) {
	item1 := BuildKey("account-1", "item-1", "front", "photo.jpg")
	item2 := BuildKey("account-1", "item-2", "front", "photo.jpg")

	prefix := "accounts/account-1/"
	if !strings.HasPrefix(item1, prefix) || !strings.HasPrefix(item2, prefix) {
		t.Errorf("expected both items under %q, got %q and %q", prefix, item1, item2)
	}
}
