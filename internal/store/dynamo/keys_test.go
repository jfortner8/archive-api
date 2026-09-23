package dynamo

import (
	"sort"
	"testing"
	"time"

	"github.com/jfortner8/archive-api/internal/domain"
)

// TestArchivesNeverShareAPartition is the property that replaces the old
// "read the row, then compare accountId in Go" check. If two archives could
// ever produce the same partition key, one family would query another's
// photographs - and nothing would error.
func TestArchivesNeverShareAPartition(t *testing.T) {
	a := domain.ArchiveID("arc_aaa")
	b := domain.ArchiveID("arc_bbb")

	if ArchivePK(a) == ArchivePK(b) {
		t.Fatal("distinct archives share a partition key")
	}
	if ItemIndexPK(a) == ItemIndexPK(b) {
		t.Fatal("distinct archives share an index partition key")
	}

	// The same item id in two archives must be two different rows.
	item := domain.ItemID("itm_same")
	if ItemKey(a, item) == ItemKey(b, item) {
		t.Fatal("the same item id collides across archives")
	}

	// An account partition must never collide with an archive partition,
	// since both live in the same table.
	if AccountPK(domain.AccountID("aaa")) == ArchivePK(domain.ArchiveID("aaa")) {
		t.Fatal("account and archive partitions collide for the same raw id")
	}
}

func TestSortKeyRoundTrips(t *testing.T) {
	archive := domain.ArchiveID("arc_1")

	item := domain.ItemID("itm_abc")
	got, err := ItemIDFromSK(ItemKey(archive, item).SK)
	if err != nil || got != item {
		t.Errorf("item round trip = %q, %v; want %q", got, err, item)
	}

	account := domain.AccountID("cognito-sub-123")
	gotAcct, err := AccountIDFromMemberSK(MemberKey(archive, account).SK)
	if err != nil || gotAcct != account {
		t.Errorf("member round trip = %q, %v; want %q", gotAcct, err, account)
	}

	gotArchive, err := ArchiveIDFromMirrorSK(MembershipMirrorKey(account, archive).SK)
	if err != nil || gotArchive != archive {
		t.Errorf("mirror round trip = %q, %v; want %q", gotArchive, err, archive)
	}

	// A sort key of the wrong entity type must be rejected, not silently
	// reinterpreted.
	if _, err := ItemIDFromSK(MemberKey(archive, account).SK); err == nil {
		t.Error("a membership sort key parsed as an item")
	}
}

// TestIndexOrdering covers the ordering the gallery, the timeline and the
// lightbox's arrow keys all depend on.
func TestIndexOrdering(t *testing.T) {
	created := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	newItem := func(id string, date *domain.ArchiveDate) *domain.ItemCard {
		item := &domain.ItemCard{ID: domain.ItemID(id), CreatedAt: created}
		if date != nil {
			n, err := date.Normalize()
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			item.Date = date
			item.DateNorm = &n
		}
		return item
	}

	older := newItem("itm_b", &domain.ArchiveDate{Kind: domain.DateExact, Date: "1952"})
	newer := newItem("itm_a", &domain.ArchiveDate{Kind: domain.DateExact, Date: "1998"})
	undated := newItem("itm_c", nil)

	keys := []string{
		ItemIndexSK(newer),
		ItemIndexSK(undated),
		ItemIndexSK(older),
	}
	sort.Strings(keys)

	// 1952, then 1998, then the undated one at its 2026 creation time.
	want := []string{ItemIndexSK(older), ItemIndexSK(newer), ItemIndexSK(undated)}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("ascending order = %v, want %v", keys, want)
		}
	}
}

// TestIndexKeysAreFixedWidth guards the reason undated items sort at their
// creation time instead of at a sentinel: dated and undated keys share one
// index, so their instant portions must be the same width or they interleave
// wrongly.
func TestIndexKeysAreFixedWidth(t *testing.T) {
	created := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

	dated := &domain.ItemCard{ID: "itm_a", CreatedAt: created}
	n, err := (&domain.ArchiveDate{Kind: domain.DateExact, Date: "1952-06-15"}).Normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	dated.DateNorm = &n

	undated := &domain.ItemCard{ID: "itm_a", CreatedAt: created}

	// Same id, so any width difference is entirely in the instant portion.
	if len(ItemIndexSK(dated)) != len(ItemIndexSK(undated)) {
		t.Fatalf("dated key %q and undated key %q differ in width",
			ItemIndexSK(dated), ItemIndexSK(undated))
	}
}

// TestTiesBreakOnID pins the total ordering. Two items on the same date must
// have a stable relative order, because the client holds an index into this
// list and steps through it.
func TestTiesBreakOnID(t *testing.T) {
	date := &domain.ArchiveDate{Kind: domain.DateExact, Date: "1952-06-15"}
	n, err := date.Normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}

	a := &domain.ItemCard{ID: "itm_aaa", Date: date, DateNorm: &n}
	b := &domain.ItemCard{ID: "itm_bbb", Date: date, DateNorm: &n}

	ka, kb := ItemIndexSK(a), ItemIndexSK(b)
	if ka == kb {
		t.Fatal("same-date items produced identical sort keys")
	}
	if !(ka < kb) {
		t.Errorf("expected %q to sort before %q", ka, kb)
	}
}
