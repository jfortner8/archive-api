package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// MatchMode decides whether repeated values for one field are OR or AND.
type MatchMode string

const (
	MatchAny MatchMode = "any"
	MatchAll MatchMode = "all"
)

// BBox is a geographic rectangle.
type BBox struct {
	MinLon, MinLat, MaxLon, MaxLat float64
}

// Contains reports whether a point falls inside. A box that crosses the
// antimeridian (MinLon > MaxLon) wraps rather than matching nothing, which is
// what a map panning past 180° actually sends.
func (b BBox) Contains(lat, lon float64) bool {
	if lat < b.MinLat || lat > b.MaxLat {
		return false
	}
	if b.MinLon <= b.MaxLon {
		return lon >= b.MinLon && lon <= b.MaxLon
	}
	return lon >= b.MinLon || lon <= b.MaxLon
}

// Filter is what every projection of the items collection takes.
//
// One struct, one parser, one Matches - used by the paginated list, the
// unpaginated spine, the facet counts and the geo aggregation alike. That is
// the whole point: the gallery, the timeline and the map bind to a single
// filter state and cross-filtering falls out rather than being built three
// times and drifting.
type Filter struct {
	// Q is free text over title, tags and subject names.
	//
	// Not notes, which is the one field a searcher most often means. Notes is
	// not carried in the secondary index, and adding it there means rebuilding
	// the index - worth doing, but a separate change with its own
	// consequences.
	Q string

	TypeIDs      []string
	Tags         []string
	TagMode      MatchMode
	SubjectIDs   []SubjectID
	SubjectMode  MatchMode
	SubjectKinds []string
	PlaceIDs     []PlaceID

	// Capabilities narrows to items that actually hold that kind of media,
	// not items whose type permits it.
	Capabilities []string

	From, To time.Time

	// DateOverlap matches an item whose date range intersects [From, To].
	// Default true: a photograph dated "1930-1960" genuinely might be the
	// 1940s one you are looking for, and strict containment would hide it.
	DateOverlap bool

	HasDate  *bool
	HasGeo   *bool
	HasFiles *bool

	BBox *BBox
}

// FacetField names a field counts can be grouped by.
type FacetField string

const (
	FacetType         FacetField = "type"
	FacetTag          FacetField = "tag"
	FacetSubject      FacetField = "subject"
	FacetPlace        FacetField = "place"
	FacetCapability   FacetField = "capability"
	FacetDateDecade   FacetField = "date:decade"
	FacetDateYear     FacetField = "date:year"
	FacetSubjectKinds FacetField = "subjectKind"
)

// filterParams is every query parameter the collection endpoints accept.
// Anything else is a 400 rather than silently ignored: a misspelled filter
// should be reported, not answered with the unfiltered set.
var filterParams = map[string]bool{
	"q": true, "typeId": true, "tag": true, "tagMode": true,
	"subjectId": true, "subjectMode": true, "subjectKind": true,
	"placeId": true, "has": true, "from": true, "to": true,
	"dateOverlap": true, "hasDate": true, "hasGeo": true, "hasFiles": true,
	"bbox": true,
}

// IsFilterParam reports whether a parameter belongs to the shared grammar, so
// each handler can allow its own extras (limit, cursor, sort, facets) on top.
func IsFilterParam(name string) bool { return filterParams[name] }

// ParseFilter reads the shared grammar out of a query string.
func ParseFilter(values url.Values) (Filter, error) {
	f := Filter{TagMode: MatchAny, SubjectMode: MatchAny, DateOverlap: true}

	f.Q = strings.TrimSpace(values.Get("q"))
	f.TypeIDs = multi(values, "typeId")
	f.Tags = normalizeTags(multi(values, "tag"))
	f.SubjectKinds = multi(values, "subjectKind")
	f.Capabilities = multi(values, "has")

	for _, id := range multi(values, "subjectId") {
		f.SubjectIDs = append(f.SubjectIDs, SubjectID(id))
	}
	for _, id := range multi(values, "placeId") {
		f.PlaceIDs = append(f.PlaceIDs, PlaceID(id))
	}

	var err error
	if f.TagMode, err = parseMode(values, "tagMode"); err != nil {
		return Filter{}, err
	}
	if f.SubjectMode, err = parseMode(values, "subjectMode"); err != nil {
		return Filter{}, err
	}

	// from/to accept the same partial ISO as a date field, so "from=1940"
	// means the start of 1940 and "to=1950" the end of 1950 rather than its
	// first instant.
	if raw := values.Get("from"); raw != "" {
		span, err := parsePartial(raw)
		if err != nil {
			return Filter{}, fmt.Errorf("from: %w", err)
		}
		f.From = span.earliest
	}
	if raw := values.Get("to"); raw != "" {
		span, err := parsePartial(raw)
		if err != nil {
			return Filter{}, fmt.Errorf("to: %w", err)
		}
		f.To = span.latest
	}
	if !f.From.IsZero() && !f.To.IsZero() && f.From.After(f.To) {
		return Filter{}, fmt.Errorf("from is after to")
	}

	if f.DateOverlap, err = parseBoolDefault(values, "dateOverlap", true); err != nil {
		return Filter{}, err
	}
	for name, target := range map[string]**bool{
		"hasDate": &f.HasDate, "hasGeo": &f.HasGeo, "hasFiles": &f.HasFiles,
	} {
		if values.Get(name) == "" {
			continue
		}
		parsed, err := parseBool(values.Get(name), name)
		if err != nil {
			return Filter{}, err
		}
		*target = &parsed
	}

	if raw := values.Get("bbox"); raw != "" {
		box, err := parseBBox(raw)
		if err != nil {
			return Filter{}, err
		}
		f.BBox = &box
	}

	return f, nil
}

// Matches reports whether a card satisfies the filter.
//
// Fields are ANDed; repeated values within a field are ORed unless that
// field's mode says all. Stated once here rather than per-endpoint, because
// three endpoints disagreeing about it is the failure this design exists to
// avoid.
func (f Filter) Matches(card *ItemCard) bool {
	if len(f.TypeIDs) > 0 && !contains(f.TypeIDs, card.TypeID) {
		return false
	}

	if len(f.PlaceIDs) > 0 {
		found := false
		for _, id := range f.PlaceIDs {
			if card.PlaceID == id {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	if !matchSet(f.Tags, normalizeTags(card.Tags), f.TagMode) {
		return false
	}

	if len(f.SubjectIDs) > 0 {
		present := make([]string, 0, len(card.Subjects))
		for _, s := range card.Subjects {
			present = append(present, string(s.ID))
		}
		wanted := make([]string, 0, len(f.SubjectIDs))
		for _, id := range f.SubjectIDs {
			wanted = append(wanted, string(id))
		}
		if !matchSet(wanted, present, f.SubjectMode) {
			return false
		}
	}

	if len(f.SubjectKinds) > 0 {
		found := false
		for _, s := range card.Subjects {
			if contains(f.SubjectKinds, s.Kind) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}

	for _, want := range f.Capabilities {
		if !contains(card.Capabilities, want) {
			return false
		}
	}

	if f.HasDate != nil && card.HasDate() != *f.HasDate {
		return false
	}
	if f.HasGeo != nil && card.Location.HasCoordinates() != *f.HasGeo {
		return false
	}
	if f.HasFiles != nil && (card.FileCount > 0) != *f.HasFiles {
		return false
	}

	if !f.matchesDate(card) {
		return false
	}

	if f.BBox != nil {
		if !card.Location.HasCoordinates() {
			return false
		}
		if !f.BBox.Contains(*card.Location.Lat, *card.Location.Lon) {
			return false
		}
	}

	return f.matchesText(card)
}

func (f Filter) matchesDate(card *ItemCard) bool {
	if f.From.IsZero() && f.To.IsZero() {
		return true
	}
	// An item with no date cannot satisfy a date window. It is reachable with
	// hasDate=false, which is also how the timeline excludes them.
	if card.DateNorm == nil {
		return false
	}
	if f.DateOverlap {
		return card.DateNorm.Overlaps(f.From, f.To)
	}

	sortAt, err := time.Parse(time.RFC3339Nano, card.DateNorm.Sort)
	if err != nil {
		return false
	}
	if !f.From.IsZero() && sortAt.Before(f.From) {
		return false
	}
	if !f.To.IsZero() && sortAt.After(f.To) {
		return false
	}
	return true
}

// matchesText is a case-insensitive substring search over title, tags and
// subject names. Not relevance ranking, stemming or typo tolerance - at a few
// thousand items this finds what you meant far more often than it misses, and
// anything better means an index this project does not need yet.
func (f Filter) matchesText(card *ItemCard) bool {
	if f.Q == "" {
		return true
	}
	needle := strings.ToLower(f.Q)

	if strings.Contains(strings.ToLower(card.Title), needle) {
		return true
	}
	for _, tag := range card.Tags {
		if strings.Contains(strings.ToLower(tag), needle) {
			return true
		}
	}
	for _, s := range card.Subjects {
		if strings.Contains(strings.ToLower(s.Name), needle) {
			return true
		}
	}
	return false
}

// Without returns the filter with one field's own constraint removed.
//
// This is what makes multi-select faceting usable. Counts for a field have to
// be computed with every filter applied EXCEPT that field's own - otherwise
// selecting one tag drops every other tag's count to zero and the user can
// never widen their selection.
func (f Filter) Without(field FacetField) Filter {
	out := f
	switch field {
	case FacetType:
		out.TypeIDs = nil
	case FacetTag:
		out.Tags = nil
	case FacetSubject:
		out.SubjectIDs = nil
	case FacetSubjectKinds:
		out.SubjectKinds = nil
	case FacetPlace:
		out.PlaceIDs = nil
	case FacetCapability:
		out.Capabilities = nil
	case FacetDateDecade, FacetDateYear:
		out.From, out.To = time.Time{}, time.Time{}
	}
	return out
}

// Hash fingerprints the filter, so a cursor can be rejected when the filters
// it was produced under have changed. Without that a client tweaks a filter,
// reuses its cursor, and is served a page of the previous query with no error
// anywhere.
func (f Filter) Hash() string {
	var b strings.Builder

	write := func(label string, values []string) {
		sorted := append([]string(nil), values...)
		sort.Strings(sorted)
		fmt.Fprintf(&b, "%s=%s;", label, strings.Join(sorted, ","))
	}

	fmt.Fprintf(&b, "q=%s;", strings.ToLower(f.Q))
	write("type", f.TypeIDs)
	write("tag", f.Tags)
	write("kind", f.SubjectKinds)
	write("cap", f.Capabilities)

	subjects := make([]string, 0, len(f.SubjectIDs))
	for _, id := range f.SubjectIDs {
		subjects = append(subjects, string(id))
	}
	write("subject", subjects)

	places := make([]string, 0, len(f.PlaceIDs))
	for _, id := range f.PlaceIDs {
		places = append(places, string(id))
	}
	write("place", places)

	fmt.Fprintf(&b, "mode=%s,%s;overlap=%t;", f.TagMode, f.SubjectMode, f.DateOverlap)
	fmt.Fprintf(&b, "from=%s;to=%s;", f.From.Format(time.RFC3339), f.To.Format(time.RFC3339))
	fmt.Fprintf(&b, "hasDate=%v;hasGeo=%v;hasFiles=%v;", boolPtr(f.HasDate), boolPtr(f.HasGeo), boolPtr(f.HasFiles))
	if f.BBox != nil {
		fmt.Fprintf(&b, "bbox=%g,%g,%g,%g;", f.BBox.MinLon, f.BBox.MinLat, f.BBox.MaxLon, f.BBox.MaxLat)
	}

	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])[:16]
}

func boolPtr(b *bool) string {
	if b == nil {
		return "-"
	}
	return strconv.FormatBool(*b)
}

// multi collects a repeated parameter, also splitting comma lists so both
// ?tag=a&tag=b and ?tag=a,b work.
func multi(values url.Values, name string) []string {
	var out []string
	for _, raw := range values[name] {
		for _, piece := range strings.Split(raw, ",") {
			if piece = strings.TrimSpace(piece); piece != "" {
				out = append(out, piece)
			}
		}
	}
	return out
}

func normalizeTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		out = append(out, strings.ToLower(strings.TrimSpace(t)))
	}
	return out
}

func parseMode(values url.Values, name string) (MatchMode, error) {
	switch raw := values.Get(name); raw {
	case "", string(MatchAny):
		return MatchAny, nil
	case string(MatchAll):
		return MatchAll, nil
	default:
		return "", fmt.Errorf("%s must be %q or %q", name, MatchAny, MatchAll)
	}
}

func parseBool(raw, name string) (bool, error) {
	switch raw {
	case "true", "1":
		return true, nil
	case "false", "0":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be true or false", name)
	}
}

func parseBoolDefault(values url.Values, name string, fallback bool) (bool, error) {
	if values.Get(name) == "" {
		return fallback, nil
	}
	return parseBool(values.Get(name), name)
}

func parseBBox(raw string) (BBox, error) {
	parts := strings.Split(raw, ",")
	if len(parts) != 4 {
		return BBox{}, fmt.Errorf("bbox must be minLon,minLat,maxLon,maxLat")
	}

	nums := make([]float64, 4)
	for i, p := range parts {
		n, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return BBox{}, fmt.Errorf("bbox: %q is not a number", p)
		}
		nums[i] = n
	}

	box := BBox{MinLon: nums[0], MinLat: nums[1], MaxLon: nums[2], MaxLat: nums[3]}
	if box.MinLat > box.MaxLat {
		return BBox{}, fmt.Errorf("bbox: minLat is above maxLat")
	}
	for _, lat := range []float64{box.MinLat, box.MaxLat} {
		if lat < -90 || lat > 90 {
			return BBox{}, fmt.Errorf("bbox: latitude must be between -90 and 90")
		}
	}
	for _, lon := range []float64{box.MinLon, box.MaxLon} {
		if lon < -180 || lon > 180 {
			return BBox{}, fmt.Errorf("bbox: longitude must be between -180 and 180")
		}
	}
	return box, nil
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// matchSet applies a field's match mode. An empty want matches everything.
func matchSet(want, have []string, mode MatchMode) bool {
	if len(want) == 0 {
		return true
	}
	if mode == MatchAll {
		for _, w := range want {
			if !contains(have, w) {
				return false
			}
		}
		return true
	}
	for _, w := range want {
		if contains(have, w) {
			return true
		}
	}
	return false
}
