package domain

import (
	"net/url"
	"testing"
)

func countFor(values []FacetValue, value string) int {
	for _, v := range values {
		if v.Value == value {
			return v.Count
		}
	}
	return 0
}

func sampleCards() []ItemCard {
	return []ItemCard{
		card("Asheville morning", func(c *ItemCard) {
			c.TypeID = "photo"
			c.Tags = []string{"asheville", "morning"}
			dated(c, DateExact, "1952")
		}),
		card("Asheville postcard", func(c *ItemCard) {
			c.TypeID = "photo-two-sided"
			c.Tags = []string{"asheville", "postcard"}
			dated(c, DateExact, "1958")
		}),
		card("Charleston pier", func(c *ItemCard) {
			c.TypeID = "photo"
			c.Tags = []string{"charleston"}
			dated(c, DateExact, "1961")
		}),
		card("A record", func(c *ItemCard) {
			c.TypeID = "cd"
			c.Tags = []string{"music"}
			c.Capabilities = []string{"image", "audio"}
			dated(c, DateExact, "1974")
		}),
	}
}

// TestFacetsExcludeTheirOwnFilter is the whole reason facets take a Filter
// rather than a pre-filtered slice.
//
// With all filters applied, choosing one tag drops every other tag's count to
// zero and the user can never widen their selection - the classic faceted
// search bug. Each field's counts have to apply everything except its own.
func TestFacetsExcludeTheirOwnFilter(t *testing.T) {
	cards := sampleCards()

	values, _ := url.ParseQuery("tag=asheville")
	selected, err := ParseFilter(values)
	if err != nil {
		t.Fatalf("ParseFilter: %v", err)
	}

	facets := ComputeFacets(cards, selected, []FacetField{FacetTag, FacetType})

	// The tag facet ignores the tag selection, so the other tags still show
	// what picking them would yield.
	tags := facets[FacetTag]
	if got := countFor(tags, "asheville"); got != 2 {
		t.Errorf("asheville count = %d, want 2", got)
	}
	if got := countFor(tags, "charleston"); got != 1 {
		t.Errorf("charleston count = %d, want 1 - selecting a tag must not zero the others", got)
	}
	if got := countFor(tags, "music"); got != 1 {
		t.Errorf("music count = %d, want 1", got)
	}

	// The type facet DOES apply the tag selection, because it is a different
	// field - so only the two Asheville items are counted.
	types := facets[FacetType]
	if got := countFor(types, "photo"); got != 1 {
		t.Errorf("photo count = %d, want 1 (only the Asheville photo)", got)
	}
	if got := countFor(types, "photo-two-sided"); got != 1 {
		t.Errorf("photo-two-sided count = %d, want 1", got)
	}
	if got := countFor(types, "cd"); got != 0 {
		t.Errorf("cd count = %d, want 0 - it is not tagged asheville", got)
	}
}

func TestFacetsWithNoFilterCountEverything(t *testing.T) {
	cards := sampleCards()
	facets := ComputeFacets(cards, Filter{}, []FacetField{FacetType, FacetTag, FacetCapability})

	if got := countFor(facets[FacetType], "photo"); got != 2 {
		t.Errorf("photo count = %d, want 2", got)
	}
	if got := countFor(facets[FacetTag], "asheville"); got != 2 {
		t.Errorf("asheville count = %d, want 2", got)
	}
	// Capabilities count the media an item actually holds.
	if got := countFor(facets[FacetCapability], "audio"); got != 1 {
		t.Errorf("audio count = %d, want 1", got)
	}
}

// TestDateFacetsDriveTheHistogram: the timeline's density and the date filter
// are the same counts, which is why this is one endpoint rather than two.
func TestDateFacetsDriveTheHistogram(t *testing.T) {
	cards := sampleCards()
	facets := ComputeFacets(cards, Filter{}, []FacetField{FacetDateDecade, FacetDateYear})

	decades := facets[FacetDateDecade]
	if got := countFor(decades, "1950"); got != 2 {
		t.Errorf("1950s count = %d, want 2", got)
	}
	if got := countFor(decades, "1960"); got != 1 {
		t.Errorf("1960s count = %d, want 1", got)
	}
	if got := countFor(decades, "1970"); got != 1 {
		t.Errorf("1970s count = %d, want 1", got)
	}

	if got := countFor(facets[FacetDateYear], "1952"); got != 1 {
		t.Errorf("1952 count = %d, want 1", got)
	}
}

// TestUndatedItemsAreInNoDateBucket: a zero bucket would sort first and read
// as a real decade.
func TestUndatedItemsAreInNoDateBucket(t *testing.T) {
	cards := append(sampleCards(), card("No date", nil))

	facets := ComputeFacets(cards, Filter{}, []FacetField{FacetDateDecade})
	for _, v := range facets[FacetDateDecade] {
		if v.Value == "0" || v.Value == "" {
			t.Errorf("undated items produced a %q bucket", v.Value)
		}
	}

	total := 0
	for _, v := range facets[FacetDateDecade] {
		total += v.Count
	}
	if total != 4 {
		t.Errorf("date buckets total %d, want 4 - the undated item should be in none", total)
	}
}

// TestSubjectFacetsCarryNames so a filter chip is a person's name rather than
// a UUID.
func TestSubjectFacetsCarryNames(t *testing.T) {
	cards := []ItemCard{
		card("One", func(c *ItemCard) {
			c.Subjects = []SubjectRef{{ID: "sub_1", Kind: "person", Name: "Ruth Fortner"}}
		}),
		card("Two", func(c *ItemCard) {
			c.Subjects = []SubjectRef{{ID: "sub_1", Kind: "person", Name: "Ruth Fortner"}}
		}),
	}

	facets := ComputeFacets(cards, Filter{}, []FacetField{FacetSubject})
	if len(facets[FacetSubject]) != 1 {
		t.Fatalf("subject facets = %v, want one entry", facets[FacetSubject])
	}
	entry := facets[FacetSubject][0]
	if entry.Value != "sub_1" || entry.Label != "Ruth Fortner" || entry.Count != 2 {
		t.Errorf("entry = %+v, want sub_1/Ruth Fortner/2", entry)
	}
}

func TestFacetsAreOrderedMostCommonFirst(t *testing.T) {
	facets := ComputeFacets(sampleCards(), Filter{}, []FacetField{FacetTag})

	values := facets[FacetTag]
	for i := 1; i < len(values); i++ {
		if values[i-1].Count < values[i].Count {
			t.Fatalf("counts are not descending: %+v", values)
		}
		// Ties break on value, so identical requests return identical order.
		if values[i-1].Count == values[i].Count && values[i-1].Value > values[i].Value {
			t.Fatalf("ties are not ordered by value: %+v", values)
		}
	}
}

func TestParseFacetFields(t *testing.T) {
	if got, err := ParseFacetFields(nil); err != nil || len(got) == 0 {
		t.Errorf("an empty request should give the defaults, got %v / %v", got, err)
	}
	if _, err := ParseFacetFields([]string{"tag", "date:decade"}); err != nil {
		t.Errorf("valid fields were rejected: %v", err)
	}
	if _, err := ParseFacetFields([]string{"nonsense"}); err == nil {
		t.Error("an unknown facet field was accepted")
	}
}
