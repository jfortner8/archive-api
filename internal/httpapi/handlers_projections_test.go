package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/jfortner8/archive-api/internal/domain"
	"github.com/jfortner8/archive-api/internal/httpapi/gen"
)

func indexPath() string  { return itemsPath() + "/index" }
func facetsPath() string { return itemsPath() + "/facets" }

type indexResponse struct {
	Data  []gen.ItemCard `json:"data"`
	Total int            `json:"total"`
}

type facetsResponse struct {
	Data    map[string][]domain.FacetValue `json:"data"`
	Total   int                            `json:"total"`
	Archive int                            `json:"archiveTotal"`
}

// photo seeds one card. Dates are years so the sort key is predictable.
func photo(t *testing.T, data *fakeStore, title, year string, build func(*domain.Item)) domain.Item {
	t.Helper()

	item := domain.Item{ItemCard: domain.ItemCard{
		TypeID: "photo", TypeVersion: 1, Title: title,
	}}
	if year != "" {
		item.Date = &domain.ArchiveDate{Kind: domain.DateExact, Date: year}
	}
	if build != nil {
		build(&item)
	}
	return data.seedItem(testArchive, item)
}

func seedGallery(t *testing.T, data *fakeStore) {
	t.Helper()

	photo(t, data, "Asheville morning", "1952", func(i *domain.Item) {
		i.Tags = []string{"asheville", "morning"}
		lat, lon := 35.5951, -82.5515
		i.Location = &domain.Location{Label: "Asheville", Lat: &lat, Lon: &lon}
	})
	photo(t, data, "Asheville postcard", "1958", func(i *domain.Item) {
		i.TypeID = "photo-two-sided"
		i.Tags = []string{"asheville", "postcard"}
	})
	photo(t, data, "Charleston pier", "1961", func(i *domain.Item) {
		i.Tags = []string{"charleston"}
		lat, lon := 32.7765, -79.9311
		i.Location = &domain.Location{Label: "Charleston", Lat: &lat, Lon: &lon}
	})
	photo(t, data, "Undated box", "", func(i *domain.Item) {
		i.Tags = []string{"unsorted"}
	})
}

// TestIndexIsUnpaginated is the point of the endpoint: the timeline lays its
// whole matching set out in one strip and the map wants every pin at once, so
// a paginated response structurally cannot feed either.
func TestIndexIsUnpaginated(t *testing.T) {
	s, data := newTestServer(t)
	for i := 0; i < 150; i++ {
		photo(t, data, fmt.Sprintf("photo %03d", i), "1950", nil)
	}

	rec := do(t, s, "GET", indexPath(), requestOpts{token: ownerToken})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	got := decode[indexResponse](t, rec)
	// 150 is past both the default page size and the maximum, so a paginated
	// handler would have truncated here.
	if len(got.Data) != 150 {
		t.Errorf("index returned %d cards, want all 150", len(got.Data))
	}
	if got.Total != 150 {
		t.Errorf("total = %d, want 150", got.Total)
	}
}

// TestIndexCoversAreOptIn: a presigned URL is several hundred high-entropy
// bytes, so attaching one to every card would dominate a response the map
// does not even use.
func TestIndexCoversAreOptIn(t *testing.T) {
	s, data := newTestServer(t)
	photo(t, data, "With a file", "1952", func(i *domain.Item) {
		i.Files = []domain.File{{
			ID: "fil_a", Role: "photo", Key: "originals/a.jpg",
			ContentType: "image/jpeg", Status: domain.FileStatusReady,
		}}
	})

	bare := decode[indexResponse](t, do(t, s, "GET", indexPath(), requestOpts{token: ownerToken}))
	if len(bare.Data) != 1 {
		t.Fatalf("card count = %d, want 1", len(bare.Data))
	}
	if bare.Data[0].CoverUrl != nil {
		t.Errorf("cover url = %q, want none without covers=true", *bare.Data[0].CoverUrl)
	}

	withCovers := decode[indexResponse](t, do(t, s, "GET", indexPath()+"?covers=true", requestOpts{token: ownerToken}))
	if len(withCovers.Data) != 1 {
		t.Fatalf("card count = %d, want 1", len(withCovers.Data))
	}
	if withCovers.Data[0].CoverUrl == nil || *withCovers.Data[0].CoverUrl == "" {
		t.Error("covers=true returned no cover url")
	}
}

// TestIndexETagSkipsTheBody matters more here than anywhere else: the
// timeline and the map both refetch the whole spine on every mount.
func TestIndexETagSkipsTheBody(t *testing.T) {
	s, data := newTestServer(t)
	seedGallery(t, data)

	first := do(t, s, "GET", indexPath(), requestOpts{token: ownerToken})
	etag := first.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag on the index")
	}

	req := do(t, s, "GET", indexPath(), requestOpts{token: ownerToken, ifNoneMatch: etag})
	if req.Code != http.StatusNotModified {
		t.Errorf("status = %d, want 304", req.Code)
	}
	if req.Body.Len() != 0 {
		t.Errorf("304 carried a %d-byte body", req.Body.Len())
	}

	// A different filter is a different response, so the same ETag must not
	// satisfy it - otherwise a client switching filters keeps its old spine.
	filtered := do(t, s, "GET", indexPath()+"?tag=asheville", requestOpts{
		token: ownerToken, ifNoneMatch: etag,
	})
	if filtered.Code != http.StatusOK {
		t.Errorf("a filtered request with the unfiltered ETag = %d, want 200", filtered.Code)
	}
	if filtered.Header().Get("ETag") == etag {
		t.Error("the filtered response reused the unfiltered ETag")
	}
}

func TestIndexAppliesTheFilter(t *testing.T) {
	s, data := newTestServer(t)
	seedGallery(t, data)

	cases := []struct {
		query string
		want  []string
	}{
		{"?tag=asheville", []string{"Asheville morning", "Asheville postcard"}},
		{"?typeId=photo-two-sided", []string{"Asheville postcard"}},
		{"?q=pier", []string{"Charleston pier"}},
		{"?hasDate=false", []string{"Undated box"}},
		{"?hasGeo=true", []string{"Asheville morning", "Charleston pier"}},
		{"?from=1955&to=1960", []string{"Asheville postcard"}},
		// A box over western North Carolina, which excludes Charleston.
		{"?bbox=-83,35,-82,36", []string{"Asheville morning"}},
	}

	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			rec := do(t, s, "GET", indexPath()+tc.query, requestOpts{token: ownerToken})
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}

			got := decode[indexResponse](t, rec)
			titles := map[string]bool{}
			for _, card := range got.Data {
				titles[card.Title] = true
			}
			if len(got.Data) != len(tc.want) {
				t.Fatalf("got %d cards %v, want %v", len(got.Data), titles, tc.want)
			}
			for _, want := range tc.want {
				if !titles[want] {
					t.Errorf("%q missing from %v", want, titles)
				}
			}
		})
	}
}

func TestIndexSortOrder(t *testing.T) {
	s, data := newTestServer(t)
	seedGallery(t, data)

	desc := decode[indexResponse](t, do(t, s, "GET", indexPath(), requestOpts{token: ownerToken}))
	asc := decode[indexResponse](t, do(t, s, "GET", indexPath()+"?sort=date:asc", requestOpts{token: ownerToken}))

	if len(desc.Data) != len(asc.Data) || len(desc.Data) == 0 {
		t.Fatalf("counts differ: %d vs %d", len(desc.Data), len(asc.Data))
	}
	for i := range desc.Data {
		if desc.Data[i].Id != asc.Data[len(asc.Data)-1-i].Id {
			t.Fatal("date:asc is not the reverse of the default order")
		}
	}
}

// TestFacetsExcludeTheirOwnFilterOverHTTP is the drill-down rule as a client
// sees it: picking a tag must leave the other tags' counts intact, or the
// sidebar becomes a dead end.
func TestFacetsExcludeTheirOwnFilterOverHTTP(t *testing.T) {
	s, data := newTestServer(t)
	seedGallery(t, data)

	rec := do(t, s, "GET", facetsPath()+"?tag=asheville&facets=tag,type", requestOpts{token: ownerToken})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	got := decode[facetsResponse](t, rec)

	count := func(field, value string) int {
		for _, v := range got.Data[field] {
			if v.Value == value {
				return v.Count
			}
		}
		return 0
	}

	if count("tag", "asheville") != 2 {
		t.Errorf("asheville = %d, want 2", count("tag", "asheville"))
	}
	if count("tag", "charleston") != 1 {
		t.Errorf("charleston = %d, want 1 - the tag facet must ignore the tag filter", count("tag", "charleston"))
	}
	// The type facet does apply the tag filter, so the Charleston photo is out
	// and only one plain photo remains.
	if count("type", "photo") != 1 {
		t.Errorf("type photo = %d, want 1", count("type", "photo"))
	}

	// Total is the matching count; archiveTotal is the whole archive, which is
	// what lets a UI say "2 of 4".
	if got.Total != 2 {
		t.Errorf("total = %d, want 2", got.Total)
	}
	if got.Archive != 4 {
		t.Errorf("archiveTotal = %d, want 4", got.Archive)
	}
}

func TestFacetsDefaultFields(t *testing.T) {
	s, data := newTestServer(t)
	seedGallery(t, data)

	got := decode[facetsResponse](t, do(t, s, "GET", facetsPath(), requestOpts{token: ownerToken}))
	for _, field := range []string{"type", "tag", "subject", "capability", "date:decade"} {
		if _, ok := got.Data[field]; !ok {
			t.Errorf("default facets missing %q (got %v)", field, keysOf(got.Data))
		}
	}
}

func TestFacetsRejectAnUnknownField(t *testing.T) {
	s, data := newTestServer(t)
	seedGallery(t, data)

	rec := do(t, s, "GET", facetsPath()+"?facets=nonsense", requestOpts{token: ownerToken})
	if rec.Code != http.StatusUnprocessableEntity && rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want a client error; body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "nonsense") {
		t.Errorf("the error does not name the bad field: %s", rec.Body.String())
	}
}

// TestMisspelledFilterIsRejected is the whole reason parseFilterFrom walks the
// query string. Answering `?taggs=beach` with the unfiltered set is a bug
// someone chases for an afternoon.
func TestMisspelledFilterIsRejected(t *testing.T) {
	s, data := newTestServer(t)
	seedGallery(t, data)

	for _, path := range []string{itemsPath(), indexPath(), facetsPath()} {
		rec := do(t, s, "GET", path+"?taggs=asheville", requestOpts{token: ownerToken})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s?taggs= = %d, want 400; body = %s", path, rec.Code, rec.Body.String())
		}
	}

	// Each endpoint's own extras are still accepted where they belong, and
	// rejected where they do not.
	if rec := do(t, s, "GET", itemsPath()+"?limit=2", requestOpts{token: ownerToken}); rec.Code != http.StatusOK {
		t.Errorf("limit on the list = %d, want 200", rec.Code)
	}
	if rec := do(t, s, "GET", indexPath()+"?limit=2", requestOpts{token: ownerToken}); rec.Code != http.StatusBadRequest {
		t.Errorf("limit on the unpaginated index = %d, want 400", rec.Code)
	}
	if rec := do(t, s, "GET", facetsPath()+"?covers=true", requestOpts{token: ownerToken}); rec.Code != http.StatusBadRequest {
		t.Errorf("covers on facets = %d, want 400", rec.Code)
	}
}

func TestListRejectsBadFilterValues(t *testing.T) {
	s, data := newTestServer(t)
	seedGallery(t, data)

	for _, query := range []string{
		"?tagMode=sometimes",
		"?from=notadate",
		"?from=1990&to=1950",
		"?bbox=1,2,3",
		"?hasDate=perhaps",
		"?sort=title:asc",
		"?limit=0",
		"?limit=5000",
	} {
		rec := do(t, s, "GET", itemsPath()+query, requestOpts{token: ownerToken})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400; body = %s", query, rec.Code, rec.Body.String())
		}
	}
}

// TestListPagesThroughAFilteredSet walks the real cursor codec over HTTP, with
// the filter applied, which is where a cursor that forgot its filter would
// show up.
func TestListPagesThroughAFilteredSet(t *testing.T) {
	s, data := newTestServer(t)
	seedGallery(t, data)
	photo(t, data, "Asheville third", "1955", func(i *domain.Item) {
		i.Tags = []string{"asheville"}
	})

	type page struct {
		Data  []gen.ItemCard `json:"data"`
		Page  gen.PageInfo   `json:"page"`
		Total int            `json:"total"`
	}

	seen := map[string]bool{}
	cursor := ""
	for round := 0; ; round++ {
		if round > 5 {
			t.Fatal("paging did not terminate")
		}
		path := itemsPath() + "?tag=asheville&limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		rec := do(t, s, "GET", path, requestOpts{token: ownerToken})
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
		}

		got := decode[page](t, rec)
		if got.Total != 3 {
			t.Errorf("total = %d, want the 3 matching items", got.Total)
		}
		for _, card := range got.Data {
			if seen[card.Id] {
				t.Errorf("item %s (%s) returned twice", card.Id, card.Title)
			}
			seen[card.Id] = true
			if !strings.HasPrefix(card.Title, "Asheville") {
				t.Errorf("%q does not match the filter", card.Title)
			}
		}
		if !got.Page.HasMore {
			break
		}
		if got.Page.Cursor == nil {
			t.Fatal("hasMore with no cursor")
		}
		cursor = *got.Page.Cursor
	}

	if len(seen) != 3 {
		t.Errorf("paged through %d items, want 3", len(seen))
	}
}

// TestCursorFromAnotherFilterIsRejected: the alternative is a page of the
// previous query's results, served with no error at all.
func TestCursorFromAnotherFilterIsRejected(t *testing.T) {
	s, data := newTestServer(t)
	seedGallery(t, data)
	photo(t, data, "Asheville third", "1955", func(i *domain.Item) {
		i.Tags = []string{"asheville"}
	})

	first := decode[struct {
		Page gen.PageInfo `json:"page"`
	}](t, do(t, s, "GET", itemsPath()+"?tag=asheville&limit=2", requestOpts{token: ownerToken}))
	if first.Page.Cursor == nil {
		t.Fatal("no cursor on a partial page")
	}

	rec := do(t, s, "GET",
		itemsPath()+"?tag=charleston&limit=2&cursor="+*first.Page.Cursor,
		requestOpts{token: ownerToken})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body = %s", rec.Code, rec.Body.String())
	}
}

// TestProjectionsAreScopedToTheArchive - the spine is read by archive, so a
// leak here would expose another family's whole collection in one request.
func TestProjectionsAreScopedToTheArchive(t *testing.T) {
	s, data := newTestServer(t)
	seedGallery(t, data)
	data.seedItem(otherArchive, domain.Item{ItemCard: domain.ItemCard{
		TypeID: "photo", TypeVersion: 1, Title: "Not yours",
	}})

	for _, path := range []string{indexPath(), facetsPath()} {
		rec := do(t, s, "GET", path, requestOpts{token: ownerToken})
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d", path, rec.Code)
		}
		if strings.Contains(rec.Body.String(), "Not yours") {
			t.Errorf("GET %s leaked an item from another archive", path)
		}
	}

	// And a non-member is refused outright rather than served an empty set,
	// which would leak that the archive exists.
	other := fmt.Sprintf("/v1/archives/%s/items/index", otherArchive)
	if rec := do(t, s, "GET", other, requestOpts{token: ownerToken}); rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
		t.Errorf("index of someone else's archive = %d, want 403 or 404", rec.Code)
	}
}

// Viewers read; the projections are reads, so they must not require more.
func TestViewersCanReadProjections(t *testing.T) {
	s, data := newTestServer(t)
	seedGallery(t, data)

	for _, path := range []string{indexPath(), facetsPath()} {
		if rec := do(t, s, "GET", path, requestOpts{token: viewerToken}); rec.Code != http.StatusOK {
			t.Errorf("viewer GET %s = %d, want 200", path, rec.Code)
		}
	}
}

func keysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
