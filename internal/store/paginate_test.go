package store

import (
	"testing"
	"time"

	"github.com/jfortner8/archive-api/internal/domain"
)

// These run without DynamoDB. Paginate is where the cursor logic lives, and
// it is pure, so the part most likely to be subtly wrong is also the part
// that needs no container to test.

func yearCard(title, year string) domain.ItemCard {
	date := domain.ArchiveDate{Kind: domain.DateExact, Date: year}
	norm, err := date.Normalize()
	if err != nil {
		panic(err)
	}
	return domain.ItemCard{
		ID:          domain.NewItemID(),
		TypeID:      "photo",
		TypeVersion: 1,
		Title:       title,
		Date:        &date,
		DateNorm:    &norm,
		CreatedAt:   time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func titles(page ItemPage) []string {
	out := make([]string, 0, len(page.Items))
	for _, card := range page.Items {
		out = append(out, card.Title)
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// descending spine, as the gallery sees it.
func spine() []domain.ItemCard {
	return []domain.ItemCard{
		yearCard("e", "2020"),
		yearCard("d", "2000"),
		yearCard("c", "1980"),
		yearCard("b", "1960"),
		yearCard("a", "1940"),
	}
}

func TestPaginateWalksEveryItemOnce(t *testing.T) {
	cards := spine()

	var seen []string
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > len(cards)+1 {
			t.Fatal("paging did not terminate")
		}
		page, err := Paginate(cards, ListQuery{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatalf("paginate: %v", err)
		}
		if page.Total != len(cards) {
			t.Errorf("total = %d, want %d - a UI says \"x of y\" with this", page.Total, len(cards))
		}
		seen = append(seen, titles(page)...)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}

	if want := []string{"e", "d", "c", "b", "a"}; !equal(seen, want) {
		t.Errorf("paged through %v, want %v", seen, want)
	}
}

// TestPaginateIsStableUnderInsert is the reason this is keyset rather than
// offset paging. Inserting an item ahead of the cursor shifts every offset;
// the client holds this ordering to drive arrow-key navigation, so a page
// that silently repeats or skips an item is a visible bug.
func TestPaginateIsStableUnderInsert(t *testing.T) {
	cards := spine()

	first, err := Paginate(cards, ListQuery{Limit: 2})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if got := titles(first); !equal(got, []string{"e", "d"}) {
		t.Fatalf("first page = %v, want [e d]", got)
	}

	// A newer item arrives at the front, where an offset of 2 would now point
	// at "d" and serve it twice.
	grown := append([]domain.ItemCard{yearCard("new", "2024")}, cards...)

	second, err := Paginate(grown, ListQuery{Limit: 2, Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if got := titles(second); !equal(got, []string{"c", "b"}) {
		t.Errorf("second page = %v, want [c b] - paging shifted under an insert", got)
	}
}

func TestPaginateAscendingSeeksTheOtherWay(t *testing.T) {
	cards := spine()
	// Ascending callers hand Paginate an ascending spine.
	for i, j := 0, len(cards)-1; i < j; i, j = i+1, j-1 {
		cards[i], cards[j] = cards[j], cards[i]
	}

	first, err := Paginate(cards, ListQuery{Limit: 2, Ascending: true})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if got := titles(first); !equal(got, []string{"a", "b"}) {
		t.Fatalf("ascending first page = %v, want [a b]", got)
	}

	second, err := Paginate(cards, ListQuery{Limit: 2, Ascending: true, Cursor: first.NextCursor})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if got := titles(second); !equal(got, []string{"c", "d"}) {
		t.Errorf("ascending second page = %v, want [c d]", got)
	}
}

// TestCursorIsRejectedWhenTheFilterChanges: the alternative is a page
// computed against the previous query, returned with no error, which is the
// kind of bug nobody notices until the counts stop adding up.
func TestCursorIsRejectedWhenTheFilterChanges(t *testing.T) {
	cards := spine()

	beach := domain.Filter{Q: "beach"}
	page, err := Paginate(cards, ListQuery{Limit: 2, Filter: beach})
	if err != nil {
		t.Fatalf("paginate: %v", err)
	}
	if page.NextCursor == "" {
		t.Fatal("expected a cursor")
	}

	if _, err := Paginate(cards, ListQuery{
		Limit: 2, Cursor: page.NextCursor, Filter: domain.Filter{Q: "mountains"},
	}); err != ErrBadCursor {
		t.Errorf("err = %v, want ErrBadCursor", err)
	}

	if _, err := Paginate(cards, ListQuery{
		Limit: 2, Cursor: page.NextCursor, Filter: beach,
	}); err != nil {
		t.Errorf("the same filter was rejected: %v", err)
	}
}

func TestPaginateRejectsAMangledCursor(t *testing.T) {
	for _, cursor := range []string{"not-base64!!", "", "eyJ2Ijo5OTl9"} {
		_, err := Paginate(spine(), ListQuery{Limit: 2, Cursor: cursor})
		if cursor == "" {
			if err != nil {
				t.Errorf("an empty cursor should mean the first page, got %v", err)
			}
			continue
		}
		if err != ErrBadCursor {
			t.Errorf("Paginate(cursor=%q) = %v, want ErrBadCursor", cursor, err)
		}
	}
}

func TestPaginateClampsTheLimit(t *testing.T) {
	cards := spine()

	page, err := Paginate(cards, ListQuery{})
	if err != nil {
		t.Fatalf("paginate: %v", err)
	}
	// Fewer items than the default limit, so no cursor: a client that sees
	// one asks for a page it will be told is empty.
	if page.NextCursor != "" {
		t.Error("a cursor was issued for a complete result")
	}
	if len(page.Items) != len(cards) {
		t.Errorf("default limit returned %d of %d", len(page.Items), len(cards))
	}

	// An absurd limit is clamped rather than refused here; the HTTP layer is
	// what rejects one outright, and this is the backstop.
	if page, err = Paginate(cards, ListQuery{Limit: 100000}); err != nil {
		t.Fatalf("paginate: %v", err)
	}
	if len(page.Items) != len(cards) {
		t.Errorf("huge limit returned %d of %d", len(page.Items), len(cards))
	}
}

func TestPaginateNeverReturnsNilItems(t *testing.T) {
	page, err := Paginate(nil, ListQuery{})
	if err != nil {
		t.Fatalf("paginate: %v", err)
	}
	// Encoded as [] rather than null, so clients need no nil-guard.
	if page.Items == nil {
		t.Error("empty page has nil Items")
	}
	if page.Total != 0 {
		t.Errorf("total = %d, want 0", page.Total)
	}
}
