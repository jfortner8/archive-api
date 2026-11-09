package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"

	"github.com/jfortner8/archive-api/internal/domain"
	"github.com/jfortner8/archive-api/internal/httpapi/gen"
)

// parseFilterFrom reads the shared grammar, rejecting any parameter that is
// neither part of it nor one of this endpoint's own extras.
//
// Rejecting rather than ignoring is deliberate: a misspelled filter silently
// answered with the unfiltered set is the kind of bug someone chases for an
// afternoon.
func parseFilterFrom(r *http.Request, extras ...string) (domain.Filter, error) {
	allowed := map[string]bool{}
	for _, name := range extras {
		allowed[name] = true
	}

	for name := range r.URL.Query() {
		if !domain.IsFilterParam(name) && !allowed[name] {
			return domain.Filter{}, errBadRequest(fmt.Sprintf("unknown query parameter %q", name))
		}
	}

	filter, err := domain.ParseFilter(r.URL.Query())
	if err != nil {
		return domain.Filter{}, errBadRequest(err.Error())
	}
	return filter, nil
}

// getItemIndex serves the spine: every matching item, unpaginated.
//
// This exists because the timeline lays its entire matching set out in one
// horizontally-scrolling strip, and the map wants every pin at once. A
// paginated endpoint structurally cannot feed either - the strip would grow
// as you scrolled, which is not a timeline.
//
// It stays small by carrying only what those two surfaces need, and by making
// cover URLs opt-in: a presigned URL is several hundred high-entropy bytes
// that compress poorly, so attaching one to every card would dominate the
// response. The map draws pins and does not want them; the timeline draws
// thumbnails and does.
func (s *Server) getItemIndex(w http.ResponseWriter, r *http.Request) error {
	member, _ := memberFrom(r.Context())

	filter, err := parseFilterFrom(r, "sort", "covers")
	if err != nil {
		return err
	}

	ascending, err := parseSort(r)
	if err != nil {
		return err
	}

	withCovers, err := parseFlag(r, "covers")
	if err != nil {
		return err
	}

	cards, err := s.Store.FilterSpine(r.Context(), member.ArchiveID, filter, ascending)
	if err != nil {
		return err
	}

	// The archive's own version plus the filter identifies this response
	// exactly, so a client that already holds it gets a 304 with no body.
	// That matters here more than anywhere: the timeline and the map both
	// fetch the whole spine on every mount.
	archive, err := s.Store.GetArchive(r.Context(), member.ArchiveID)
	if err != nil {
		return err
	}
	etag := spineETag(archive.Version, filter.Hash(), ascending, withCovers, len(cards))
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return nil
	}

	urls := fileURLs{}
	if withCovers {
		urls = s.resolveCoverURLs(r.Context(), cards)
	}

	out := make([]gen.ItemCard, 0, len(cards))
	for i := range cards {
		out = append(out, toGenCard(cards[i], urls))
	}

	return writeJSON(w, http.StatusOK, struct {
		Data  []gen.ItemCard `json:"data"`
		Total int            `json:"total"`
	}{Data: out, Total: len(out)})
}

// getItemFacets counts the matching set by category.
//
// Drives both the filter sidebar and the timeline's histogram, which is why
// it is one endpoint rather than two: the date buckets a histogram draws are
// the same counts a date filter needs.
func (s *Server) getItemFacets(w http.ResponseWriter, r *http.Request) error {
	member, _ := memberFrom(r.Context())

	filter, err := parseFilterFrom(r, "facets")
	if err != nil {
		return err
	}

	fields, err := domain.ParseFacetFields(multiParam(r, "facets"))
	if err != nil {
		var fieldErr *domain.FieldError
		if asFieldError(err, &fieldErr) {
			return errValidation(domain.ValidationErrors{*fieldErr})
		}
		return errBadRequest(err.Error())
	}

	// Facets are counted over the whole spine, not a filtered slice: each
	// field's counts apply every filter except its own, so the computation
	// needs every card plus the filter.
	cards, err := s.Store.Spine(r.Context(), member.ArchiveID, false)
	if err != nil {
		return err
	}

	facets := domain.ComputeFacets(cards, filter, fields)

	matching := 0
	for i := range cards {
		if filter.Matches(&cards[i]) {
			matching++
		}
	}

	out := map[string][]domain.FacetValue{}
	for field, values := range facets {
		out[string(field)] = values
	}

	return writeJSON(w, http.StatusOK, struct {
		Data    map[string][]domain.FacetValue `json:"data"`
		Total   int                            `json:"total"`
		Archive int                            `json:"archiveTotal"`
	}{Data: out, Total: matching, Archive: len(cards)})
}

func parseSort(r *http.Request) (ascending bool, err error) {
	switch r.URL.Query().Get("sort") {
	case "", "date:desc":
		return false, nil
	case "date:asc":
		return true, nil
	default:
		return false, errBadRequest(`sort must be "date:desc" or "date:asc"`)
	}
}

func parseFlag(r *http.Request, name string) (bool, error) {
	switch r.URL.Query().Get(name) {
	case "", "false", "0":
		return false, nil
	case "true", "1":
		return true, nil
	default:
		return false, errBadRequest(name + " must be true or false")
	}
}

func parseLimit(r *http.Request) (int, error) {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return 0, nil
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit < 1 || limit > 200 {
		return 0, errBadRequest("limit must be between 1 and 200")
	}
	return limit, nil
}

func multiParam(r *http.Request, name string) []string {
	var out []string
	for _, raw := range r.URL.Query()[name] {
		for _, piece := range splitComma(raw) {
			out = append(out, piece)
		}
	}
	return out
}

func splitComma(raw string) []string {
	var out []string
	current := ""
	for _, ch := range raw {
		if ch == ',' {
			if current != "" {
				out = append(out, current)
			}
			current = ""
			continue
		}
		current += string(ch)
	}
	if current != "" {
		out = append(out, current)
	}
	return out
}

func spineETag(version int64, filterHash string, ascending, covers bool, count int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d|%s|%t|%t|%d", version, filterHash, ascending, covers, count)))
	return `W/"` + hex.EncodeToString(sum[:])[:16] + `"`
}

// asFieldError unwraps a domain.FieldError without pulling errors.As into
// every call site.
func asFieldError(err error, target **domain.FieldError) bool {
	if fe, ok := err.(*domain.FieldError); ok {
		*target = fe
		return true
	}
	return false
}
