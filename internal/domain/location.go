package domain

import "fmt"

// ConfidenceRadius is how precisely a location is known. These values are the
// wire contract with archives-ui, which keys its map circles off them.
type ConfidenceRadius string

const (
	ConfidenceExact        ConfidenceRadius = "exact"
	ConfidenceNeighborhood ConfidenceRadius = "neighborhood"
	ConfidenceCity         ConfidenceRadius = "city"
	ConfidenceRegion       ConfidenceRadius = "region"
	ConfidenceState        ConfidenceRadius = "state"
	ConfidenceCountry      ConfidenceRadius = "country"
)

// confidenceMeters is how far from the stated point the subject might
// actually have been.
//
// The client currently sizes its circles in screen pixels, which means an
// item's uncertainty visually shrinks as you zoom in - backwards, since
// zooming in should make a vague location look vaguer relative to the
// streets around it. Owning the real-world radius here lets the map draw a
// true-to-scale circle with a pixel floor, and means the renderer, the
// decision about what may be aggregated into a hexbin, and any future
// "which place is this" logic all share one definition instead of three.
var confidenceMeters = map[ConfidenceRadius]int{
	ConfidenceExact:        50,
	ConfidenceNeighborhood: 1_000,
	ConfidenceCity:         10_000,
	ConfidenceRegion:       50_000,
	ConfidenceState:        300_000,
	ConfidenceCountry:      2_000_000,
}

// Location is where something happened. Lat/Lon/Confidence are optional: a
// location can be nothing but a text label, and in a real archive it very
// often is ("the old house", "somewhere in Kansas").
type Location struct {
	Label      string           `json:"label" dynamodbav:"label"`
	Lat        *float64         `json:"lat,omitempty" dynamodbav:"lat,omitempty"`
	Lon        *float64         `json:"lon,omitempty" dynamodbav:"lon,omitempty"`
	Confidence ConfidenceRadius `json:"confidence,omitempty" dynamodbav:"confidence,omitempty"`

	// RadiusMeters is derived from Confidence and returned read-only.
	RadiusMeters int `json:"radiusMeters,omitempty" dynamodbav:"radiusMeters,omitempty"`
}

// HasCoordinates reports whether this location can be drawn on a map at all.
// An item with only a label gets no pin, which is why the map silently drops
// some items today.
func (l *Location) HasCoordinates() bool {
	return l != nil && l.Lat != nil && l.Lon != nil
}

// Validate rejects an unknown confidence value.
//
// This one matters more than it looks. The client falls back to its
// most-precise rendering for any confidence it doesn't recognise, so a typo
// like "citty" doesn't degrade to "we're not sure" - it renders as a
// pinpoint. A mistake becomes a confident false claim, which is the worst
// possible direction for an archive to fail in.
func (l *Location) Validate() error {
	if l == nil {
		return nil
	}
	if l.Label == "" {
		return fmt.Errorf("location.label is required")
	}
	if (l.Lat == nil) != (l.Lon == nil) {
		return fmt.Errorf("location.lat and location.lon must be given together")
	}
	if l.Lat != nil && (*l.Lat < -90 || *l.Lat > 90) {
		return fmt.Errorf("location.lat must be between -90 and 90")
	}
	if l.Lon != nil && (*l.Lon < -180 || *l.Lon > 180) {
		return fmt.Errorf("location.lon must be between -180 and 180")
	}
	if l.Confidence != "" {
		if _, ok := confidenceMeters[l.Confidence]; !ok {
			return fmt.Errorf("location.confidence %q is not a known confidence level", l.Confidence)
		}
		if l.Lat == nil {
			return fmt.Errorf("location.confidence only applies once lat and lon are set")
		}
	}
	return nil
}

// Normalize fills in the derived radius. Unset confidence means "exact",
// matching how the client already reads it.
func (l *Location) Normalize() {
	if l == nil || l.Lat == nil {
		return
	}
	confidence := l.Confidence
	if confidence == "" {
		confidence = ConfidenceExact
	}
	l.RadiusMeters = confidenceMeters[confidence]
}

// Binnable reports whether this location may be aggregated into a hexbin.
//
// Only confident locations may be. Dropping an item known only to the country
// into a three-kilometre hexagon would assert a precision nobody claimed -
// the aggregate would look like real data. Coarse locations stay as
// individual translucent circles, which is what the current map already
// draws.
func (l *Location) Binnable() bool {
	if !l.HasCoordinates() {
		return false
	}
	switch l.Confidence {
	case "", ConfidenceExact, ConfidenceNeighborhood, ConfidenceCity:
		return true
	default:
		return false
	}
}
