package domain

import (
	"sort"
	"strconv"
	"time"
)

// FacetValue is one bucket and how many items fall in it.
type FacetValue struct {
	Value string `json:"value"`

	// Label is a human-readable name where the value is an id - a subject's
	// name against its id, say - so a filter chip does not have to be a UUID.
	Label string `json:"label,omitempty"`

	Count int `json:"count"`
}

// Facets is the counts for each requested field.
type Facets map[FacetField][]FacetValue

// DefaultFacetFields is what a client gets when it asks for facets without
// saying which.
func DefaultFacetFields() []FacetField {
	return []FacetField{FacetType, FacetTag, FacetSubject, FacetCapability, FacetDateDecade}
}

// ParseFacetFields validates a requested list.
func ParseFacetFields(requested []string) ([]FacetField, error) {
	if len(requested) == 0 {
		return DefaultFacetFields(), nil
	}

	known := map[FacetField]bool{
		FacetType: true, FacetTag: true, FacetSubject: true, FacetPlace: true,
		FacetCapability: true, FacetDateDecade: true, FacetDateYear: true,
		FacetSubjectKinds: true,
	}

	out := make([]FacetField, 0, len(requested))
	for _, raw := range requested {
		field := FacetField(raw)
		if !known[field] {
			return nil, &FieldError{
				Field: "facets", Code: "unknown_facet",
				Message: strconv.Quote(raw) + " is not a facet this API counts",
			}
		}
		out = append(out, field)
	}
	return out, nil
}

// ComputeFacets counts each field over the cards.
//
// The important part is that each field's counts are computed with every
// filter applied EXCEPT that field's own. Applying all of them would drop
// every unselected value to zero the moment one is chosen, leaving the user
// unable to widen their selection - the classic faceted-search bug, and the
// reason this takes a Filter rather than a pre-filtered slice.
func ComputeFacets(cards []ItemCard, base Filter, fields []FacetField) Facets {
	out := Facets{}

	for _, field := range fields {
		narrowed := base.Without(field)

		counts := map[string]int{}
		labels := map[string]string{}

		for i := range cards {
			card := &cards[i]
			if !narrowed.Matches(card) {
				continue
			}
			for _, bucket := range bucketsFor(card, field) {
				counts[bucket.Value]++
				if bucket.Label != "" {
					labels[bucket.Value] = bucket.Label
				}
			}
		}

		values := make([]FacetValue, 0, len(counts))
		for value, count := range counts {
			values = append(values, FacetValue{Value: value, Label: labels[value], Count: count})
		}

		// Most common first, then by value, so the order is stable between
		// identical requests and a client can show a sensible top-N.
		sort.Slice(values, func(i, j int) bool {
			if values[i].Count != values[j].Count {
				return values[i].Count > values[j].Count
			}
			return values[i].Value < values[j].Value
		})

		out[field] = values
	}

	return out
}

// bucketsFor returns the values one card contributes to one facet. A card can
// contribute several (it has several tags) or none (it has no date).
func bucketsFor(card *ItemCard, field FacetField) []FacetValue {
	switch field {
	case FacetType:
		return []FacetValue{{Value: card.TypeID}}

	case FacetTag:
		out := make([]FacetValue, 0, len(card.Tags))
		for _, tag := range card.Tags {
			out = append(out, FacetValue{Value: tag})
		}
		return out

	case FacetSubject:
		out := make([]FacetValue, 0, len(card.Subjects))
		for _, s := range card.Subjects {
			out = append(out, FacetValue{Value: string(s.ID), Label: s.Name})
		}
		return out

	case FacetSubjectKinds:
		seen := map[string]bool{}
		var out []FacetValue
		for _, s := range card.Subjects {
			if !seen[s.Kind] {
				seen[s.Kind] = true
				out = append(out, FacetValue{Value: s.Kind})
			}
		}
		return out

	case FacetPlace:
		if card.PlaceID == "" {
			return nil
		}
		label := ""
		if card.Location != nil {
			label = card.Location.Label
		}
		return []FacetValue{{Value: string(card.PlaceID), Label: label}}

	case FacetCapability:
		out := make([]FacetValue, 0, len(card.Capabilities))
		for _, c := range card.Capabilities {
			out = append(out, FacetValue{Value: c})
		}
		return out

	case FacetDateDecade, FacetDateYear:
		// Undated items contribute to no date bucket rather than to a "0"
		// one, which would sort first and read as a real decade.
		if card.DateNorm == nil {
			return nil
		}
		at, err := time.Parse(time.RFC3339Nano, card.DateNorm.Sort)
		if err != nil {
			return nil
		}
		if field == FacetDateYear {
			return []FacetValue{{Value: strconv.Itoa(at.Year())}}
		}
		return []FacetValue{{Value: strconv.Itoa((at.Year() / 10) * 10)}}
	}

	return nil
}
