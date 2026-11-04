package domain

import (
	"net/url"
	"testing"
)

func card(title string, build func(*ItemCard)) ItemCard {
	c := ItemCard{
		ID:     NewItemID(),
		TypeID: "photo",
		Title:  title,
		Tags:   []string{},
	}
	if build != nil {
		build(&c)
	}
	return c
}

func dated(c *ItemCard, kind DateKind, value string) {
	d := ArchiveDate{Kind: kind, Date: value}
	n, err := d.Normalize()
	if err != nil {
		panic(err)
	}
	c.Date = &d
	c.DateNorm = &n
}

func at(lat, lon float64) *Location {
	return &Location{Label: "somewhere", Lat: &lat, Lon: &lon}
}

func parse(t *testing.T, query string) Filter {
	t.Helper()
	values, err := url.ParseQuery(query)
	if err != nil {
		t.Fatalf("bad test query %q: %v", query, err)
	}
	f, err := ParseFilter(values)
	if err != nil {
		t.Fatalf("ParseFilter(%q): %v", query, err)
	}
	return f
}

func TestFilterFieldsAreAnded(t *testing.T) {
	c := card("Blue Ridge", func(c *ItemCard) {
		c.Tags = []string{"mountains", "morning"}
		dated(c, DateExact, "1952")
	})

	if !parse(t, "tag=mountains&typeId=photo").Matches(&c) {
		t.Error("an item satisfying both fields did not match")
	}
	// The type is wrong, so the matching tag must not rescue it.
	if parse(t, "tag=mountains&typeId=document").Matches(&c) {
		t.Error("fields were ORed; they must be ANDed")
	}
}

func TestRepeatedValuesAreOredUnlessModeSaysAll(t *testing.T) {
	c := card("Beach", func(c *ItemCard) { c.Tags = []string{"beach", "summer"} })

	if !parse(t, "tag=beach&tag=winter").Matches(&c) {
		t.Error("any-mode should match on one of the tags")
	}
	if parse(t, "tag=beach&tag=winter&tagMode=all").Matches(&c) {
		t.Error("all-mode matched an item missing one of the tags")
	}
	if !parse(t, "tag=beach&tag=summer&tagMode=all").Matches(&c) {
		t.Error("all-mode rejected an item carrying both tags")
	}
	// Comma lists and repeats are the same thing.
	if !parse(t, "tag=beach,winter").Matches(&c) {
		t.Error("a comma-separated list behaved differently from repeats")
	}
}

func TestTagMatchingIgnoresCase(t *testing.T) {
	c := card("Asheville", func(c *ItemCard) { c.Tags = []string{"Asheville"} })
	if !parse(t, "tag=asheville").Matches(&c) {
		t.Error("tag matching was case-sensitive")
	}
}

// TestDateOverlapIsTheDefault is the behaviour a containment test would get
// wrong: a photograph dated 1930-1960 genuinely might be the 1940s one being
// looked for.
func TestDateOverlapIsTheDefault(t *testing.T) {
	wide := card("Wide range", func(c *ItemCard) {
		d := ArchiveDate{Kind: DateRange, Start: "1930", End: "1960"}
		n, _ := d.Normalize()
		c.Date, c.DateNorm = &d, &n
	})

	if !parse(t, "from=1940&to=1950").Matches(&wide) {
		t.Error("a range spanning the window did not match by default")
	}
	// Its midpoint is 1945, so strict containment also matches here; use a
	// range whose midpoint falls outside to tell them apart.
	lopsided := card("Lopsided", func(c *ItemCard) {
		d := ArchiveDate{Kind: DateRange, Start: "1900", End: "1945"}
		n, _ := d.Normalize()
		c.Date, c.DateNorm = &d, &n
	})
	if !parse(t, "from=1940&to=1950").Matches(&lopsided) {
		t.Error("overlap mode missed a range that reaches into the window")
	}
	if parse(t, "from=1940&to=1950&dateOverlap=false").Matches(&lopsided) {
		t.Error("containment mode matched a range whose midpoint is outside the window")
	}
}

func TestToIsInclusiveToTheEndOfItsPrecision(t *testing.T) {
	december := card("December", func(c *ItemCard) { dated(c, DateExact, "1950-12-31") })

	// "to=1950" has to mean the end of 1950, not its first instant, or the
	// last day of the year falls outside a filter that plainly includes it.
	if !parse(t, "from=1940&to=1950").Matches(&december) {
		t.Error("to= excluded the end of its own year")
	}
}

func TestUndatedItemsNeedHasDate(t *testing.T) {
	undated := card("No date", nil)

	if parse(t, "from=1940&to=1950").Matches(&undated) {
		t.Error("an undated item satisfied a date window")
	}
	if !parse(t, "hasDate=false").Matches(&undated) {
		t.Error("hasDate=false did not reach an undated item")
	}
	if parse(t, "hasDate=true").Matches(&undated) {
		t.Error("hasDate=true matched an undated item")
	}
}

func TestSubjectFiltering(t *testing.T) {
	grandma := SubjectID("sub_grandma")
	rufus := SubjectID("sub_rufus")

	c := card("Family", func(c *ItemCard) {
		c.Subjects = []SubjectRef{
			{ID: grandma, Kind: "person", Name: "Ruth Fortner"},
			{ID: rufus, Kind: "pet", Name: "Rufus"},
		}
	})

	if !parse(t, "subjectId="+string(grandma)).Matches(&c) {
		t.Error("filtering by subject id missed a tagged item")
	}
	if !parse(t, "subjectKind=pet").Matches(&c) {
		t.Error("filtering by subject kind missed an item tagged with a pet")
	}
	if parse(t, "subjectKind=organisation").Matches(&c) {
		t.Error("an unmatched subject kind still matched")
	}
	if !parse(t, "subjectId="+string(grandma)+"&subjectId="+string(rufus)+"&subjectMode=all").Matches(&c) {
		t.Error("all-mode rejected an item tagged with both subjects")
	}
	if parse(t, "subjectId="+string(grandma)+"&subjectId=sub_nobody&subjectMode=all").Matches(&c) {
		t.Error("all-mode matched an item missing one subject")
	}
}

// TestTextSearchCoversNamesAndTags is what makes the search box useful on an
// archive: people search for who is in a photograph, not just its title.
func TestTextSearchCoversNamesAndTags(t *testing.T) {
	c := card("IMG_0042", func(c *ItemCard) {
		c.Tags = []string{"spanish-moss"}
		c.Subjects = []SubjectRef{{ID: "sub_1", Kind: "person", Name: "Ruth Fortner"}}
	})

	for _, q := range []string{"img_0042", "IMG", "moss", "ruth", "Fortner"} {
		if !parse(t, "q="+url.QueryEscape(q)).Matches(&c) {
			t.Errorf("q=%q did not match", q)
		}
	}
	if parse(t, "q=charleston").Matches(&c) {
		t.Error("an unrelated term matched")
	}
}

func TestCapabilityFilterIsAnded(t *testing.T) {
	both := card("CD", func(c *ItemCard) { c.Capabilities = []string{"image", "audio"} })
	imageOnly := card("Photo", func(c *ItemCard) { c.Capabilities = []string{"image"} })

	if !parse(t, "has=audio").Matches(&both) {
		t.Error("has=audio missed an item that holds audio")
	}
	if parse(t, "has=audio").Matches(&imageOnly) {
		t.Error("has=audio matched an item with no audio")
	}
	// Several capabilities are AND: "something I can both look at and listen to".
	if !parse(t, "has=image&has=audio").Matches(&both) {
		t.Error("two capabilities rejected an item holding both")
	}
	if parse(t, "has=image&has=audio").Matches(&imageOnly) {
		t.Error("two capabilities matched an item holding one")
	}
}

func TestBBoxFiltering(t *testing.T) {
	asheville := card("Asheville", func(c *ItemCard) { c.Location = at(35.5951, -82.5515) })
	labelOnly := card("Label only", func(c *ItemCard) { c.Location = &Location{Label: "the old house"} })

	if !parse(t, "bbox=-83,35,-82,36").Matches(&asheville) {
		t.Error("a point inside the box did not match")
	}
	if parse(t, "bbox=-10,35,10,36").Matches(&asheville) {
		t.Error("a point outside the box matched")
	}
	// An item with only a text label has no position, so it cannot be in any
	// box - this is why the map silently drops some items.
	if parse(t, "bbox=-180,-90,180,90").Matches(&labelOnly) {
		t.Error("an item without coordinates matched a bounding box")
	}
}

// TestBBoxWrapsTheAntimeridian covers what a map panned past 180° actually
// sends: a box whose minimum longitude exceeds its maximum.
func TestBBoxWrapsTheAntimeridian(t *testing.T) {
	fiji := card("Fiji", func(c *ItemCard) { c.Location = at(-17.7, 178.0) })
	samoa := card("Samoa", func(c *ItemCard) { c.Location = at(-13.8, -172.0) })
	paris := card("Paris", func(c *ItemCard) { c.Location = at(48.85, 2.35) })

	wrapping := parse(t, "bbox=170,-25,-170,-5")
	if !wrapping.Matches(&fiji) {
		t.Error("a wrapping box missed a point east of 170")
	}
	if !wrapping.Matches(&samoa) {
		t.Error("a wrapping box missed a point west of -170")
	}
	if wrapping.Matches(&paris) {
		t.Error("a wrapping box matched a point nowhere near it")
	}
}

func TestParseFilterRejectsNonsense(t *testing.T) {
	tests := []string{
		"tagMode=sometimes",
		"from=notadate",
		"from=1990&to=1950",
		"bbox=1,2,3",
		"bbox=1,2,3,notanumber",
		"bbox=-10,95,10,96",
		"hasDate=perhaps",
		"dateOverlap=maybe",
	}

	for _, query := range tests {
		values, err := url.ParseQuery(query)
		if err != nil {
			t.Fatalf("bad test query: %v", err)
		}
		if _, err := ParseFilter(values); err == nil {
			t.Errorf("ParseFilter(%q) succeeded, want an error", query)
		}
	}
}

// TestHashChangesWithTheFilter is what makes a cursor safe: reusing one after
// changing a filter has to be detectable, or the client is served a page of
// the previous query with no error anywhere.
func TestHashChangesWithTheFilter(t *testing.T) {
	base := parse(t, "tag=beach")

	for _, other := range []string{
		"tag=mountains", "tag=beach&typeId=photo", "tag=beach&tagMode=all",
		"tag=beach&from=1940", "tag=beach&hasGeo=true", "q=beach",
	} {
		if parse(t, other).Hash() == base.Hash() {
			t.Errorf("%q hashes the same as the base filter", other)
		}
	}

	// Order and spelling must not change it, or a cursor breaks on a
	// semantically identical request.
	if parse(t, "tag=beach&tag=summer").Hash() != parse(t, "tag=summer&tag=beach").Hash() {
		t.Error("value order changed the hash")
	}
	if parse(t, "tag=BEACH").Hash() != base.Hash() {
		t.Error("tag case changed the hash")
	}
}

func TestUnknownParameterIsNotAFilterParam(t *testing.T) {
	if IsFilterParam("tagg") {
		t.Error("a misspelled parameter was accepted as part of the grammar")
	}
	for _, name := range []string{"q", "tag", "subjectId", "bbox", "from", "to", "has"} {
		if !IsFilterParam(name) {
			t.Errorf("%q should be part of the grammar", name)
		}
	}
}
