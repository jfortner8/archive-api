package domain

import (
	"fmt"
	"strings"
	"time"
)

// DateKind selects which of an ArchiveDate's fields apply.
type DateKind string

const (
	// DateExact means Date holds the day - or the month, or the year. An
	// archive is full of "1952" with no more detail, and pretending that
	// means 1 January would be a claim nobody made.
	DateExact DateKind = "exact"
	// DateRange means Start and End bound it.
	DateRange DateKind = "range"
	// DateCirca means Date is the centre point and Unit says how wide the
	// uncertainty around it is.
	DateCirca DateKind = "circa"
)

// CircaUnit is how wide a circa date's uncertainty is. These values are the
// wire contract with archives-ui, whose timeline keys its "fuzzy band" pixel
// widths off them - so they are spelled exactly as the UI spells them.
type CircaUnit string

const (
	UnitMonth       CircaUnit = "month"
	UnitThreeMonths CircaUnit = "3-months"
	UnitYear        CircaUnit = "year"
	UnitFiveYears   CircaUnit = "5-years"
	UnitDecade      CircaUnit = "decade"
	UnitCentury     CircaUnit = "century"
)

// circaHalfWidth is how far either side of the centre a circa date reaches.
var circaHalfWidth = map[CircaUnit]time.Duration{
	UnitMonth:       15 * 24 * time.Hour,
	UnitThreeMonths: 46 * 24 * time.Hour,
	UnitYear:        183 * 24 * time.Hour,
	UnitFiveYears:   913 * 24 * time.Hour,
	UnitDecade:      1826 * 24 * time.Hour,
	UnitCentury:     18262 * 24 * time.Hour,
}

// ArchiveDate is when something happened, which is very often not a single
// known day.
//
// The JSON shape here is byte-identical to what archives-ui already sends and
// renders - the UI reuses these types verbatim, so the discriminator and both
// enums must round-trip exactly. Everything derived from them lives in
// Normalized instead, so nothing about this shape has to change.
type ArchiveDate struct {
	Kind  DateKind  `json:"kind" dynamodbav:"kind"`
	Date  string    `json:"date,omitempty" dynamodbav:"date,omitempty"`
	Start string    `json:"start,omitempty" dynamodbav:"start,omitempty"`
	End   string    `json:"end,omitempty" dynamodbav:"end,omitempty"`
	Unit  CircaUnit `json:"unit,omitempty" dynamodbav:"unit,omitempty"`
}

// Precision records how exactly a date is known, independently of which Kind
// expressed it. "1952" and "circa 1952, ±1 year" are both year-precision.
type Precision string

const (
	PrecisionDay     Precision = "day"
	PrecisionMonth   Precision = "month"
	PrecisionYear    Precision = "year"
	PrecisionDecade  Precision = "decade"
	PrecisionCentury Precision = "century"
)

// Normalized is everything the server derives from an ArchiveDate. It is
// stored alongside the original, projected into the index, and returned
// read-only.
//
// This exists so the client stops parsing dates. Today archives-ui sorts with
// localeCompare over ISO strings, which quietly mis-orders "1952" against
// "1952-01-15", and groups timeline entries by year only if someone
// remembered to sort first. With a server-computed sort key, order is a
// guarantee rather than a hope.
type Normalized struct {
	// Sort is a fixed-width RFC3339 instant used as the index sort key. It is
	// the midpoint of Earliest and Latest - see SortKey for why midpoint.
	Sort string `json:"dateSort" dynamodbav:"dateSort"`

	// Earliest and Latest bound the date. "Overlaps 1940-1950" is then just
	// Earliest <= to AND Latest >= from.
	Earliest string `json:"dateEarliest" dynamodbav:"dateEarliest"`
	Latest   string `json:"dateLatest" dynamodbav:"dateLatest"`

	Precision Precision `json:"datePrecision" dynamodbav:"datePrecision"`

	// BandTier is which of the UI's fuzzy-band widths to draw. Computed here
	// for every Kind - including range, which the client currently buckets
	// itself - so one definition serves circa and range alike.
	BandTier CircaUnit `json:"dateBandTier" dynamodbav:"dateBandTier"`
}

// Validate reports whether the date is well formed.
//
// This is stricter than what came before on purpose. The old handler accepted
// {"kind":"whenever"} and even {} and persisted them, and on the read side the
// UI falls back to its most-precise rendering for anything it doesn't
// recognise - so a typo became a confident claim of precision. Rejecting at
// the door is the only place that asymmetry can be fixed.
func (d *ArchiveDate) Validate() error {
	switch d.Kind {
	case DateExact:
		if d.Date == "" {
			return fmt.Errorf("date.date is required when kind is %q", DateExact)
		}
		if d.Start != "" || d.End != "" || d.Unit != "" {
			return fmt.Errorf("date.start, date.end and date.unit do not apply when kind is %q", DateExact)
		}
		if _, err := parsePartial(d.Date); err != nil {
			return fmt.Errorf("date.date: %w", err)
		}

	case DateRange:
		if d.Start == "" || d.End == "" {
			return fmt.Errorf("date.start and date.end are required when kind is %q", DateRange)
		}
		if d.Date != "" || d.Unit != "" {
			return fmt.Errorf("date.date and date.unit do not apply when kind is %q", DateRange)
		}
		start, err := parsePartial(d.Start)
		if err != nil {
			return fmt.Errorf("date.start: %w", err)
		}
		end, err := parsePartial(d.End)
		if err != nil {
			return fmt.Errorf("date.end: %w", err)
		}
		if start.earliest.After(end.latest) {
			return fmt.Errorf("date.start is after date.end")
		}

	case DateCirca:
		if d.Date == "" {
			return fmt.Errorf("date.date is required when kind is %q", DateCirca)
		}
		if d.Start != "" || d.End != "" {
			return fmt.Errorf("date.start and date.end do not apply when kind is %q", DateCirca)
		}
		if _, err := parsePartial(d.Date); err != nil {
			return fmt.Errorf("date.date: %w", err)
		}
		// Unit is optional: "circa 1960" and "circa May 1975" are how people
		// actually write these down, and demanding a width turns a natural
		// note into a form to fill in. When it is given it still has to be a
		// real one, since an unrecognised width would otherwise be silently
		// ignored.
		if d.Unit != "" {
			if _, ok := circaHalfWidth[d.Unit]; !ok {
				return fmt.Errorf("date.unit %q is not a known circa unit", d.Unit)
			}
		}

	default:
		return fmt.Errorf("date.kind %q must be one of %q, %q or %q", d.Kind, DateExact, DateRange, DateCirca)
	}

	return nil
}

// Normalize derives the sortable and queryable form. The date must already
// have passed Validate.
func (d *ArchiveDate) Normalize() (Normalized, error) {
	if err := d.Validate(); err != nil {
		return Normalized{}, err
	}

	var earliest, latest time.Time

	switch d.Kind {
	case DateExact:
		span, _ := parsePartial(d.Date)
		earliest, latest = span.earliest, span.latest

	case DateRange:
		start, _ := parsePartial(d.Start)
		end, _ := parsePartial(d.End)
		earliest, latest = start.earliest, end.latest

	case DateCirca:
		// The centre is the midpoint of whatever precision was given, so
		// "circa 1952" centres on mid-1952 rather than on New Year's Day.
		span, _ := parsePartial(d.Date)
		centre := midpoint(span.earliest, span.latest)
		half := circaHalfWidth[d.ResolvedUnit()]
		earliest, latest = centre.Add(-half), centre.Add(half)
	}

	return Normalized{
		Sort:      formatInstant(midpoint(earliest, latest)),
		Earliest:  formatInstant(earliest),
		Latest:    formatInstant(latest),
		Precision: precisionFor(earliest, latest),
		BandTier:  bandTierFor(latest.Sub(earliest)),
	}, nil
}

// Overlaps reports whether this date's span intersects [from, to]. Either
// bound may be zero to leave that side open.
func (n Normalized) Overlaps(from, to time.Time) bool {
	earliest, err1 := time.Parse(time.RFC3339Nano, n.Earliest)
	latest, err2 := time.Parse(time.RFC3339Nano, n.Latest)
	if err1 != nil || err2 != nil {
		return false
	}
	if !to.IsZero() && earliest.After(to) {
		return false
	}
	if !from.IsZero() && latest.Before(from) {
		return false
	}
	return true
}

// partialSpan is the range of instants a partial ISO date covers.
type partialSpan struct {
	earliest time.Time
	latest   time.Time
	// layout records how much detail was given, which is what makes "1952"
	// year-precision rather than a day that happens to be 1 January.
	layout Precision
}

// parsePartial accepts YYYY, YYYY-MM and YYYY-MM-DD, because an archive is
// full of dates known only to the year or the month. The old code assumed
// full ISO everywhere and sorted lexically, which put "1952" before
// "1952-01-15" by accident rather than by decision.
func parsePartial(s string) (partialSpan, error) {
	s = strings.TrimSpace(s)

	switch len(s) {
	case 4:
		t, err := time.Parse("2006", s)
		if err != nil {
			return partialSpan{}, fmt.Errorf("%q is not a valid year", s)
		}
		return partialSpan{
			earliest: t,
			latest:   t.AddDate(1, 0, 0).Add(-time.Nanosecond),
			layout:   PrecisionYear,
		}, nil

	case 7:
		t, err := time.Parse("2006-01", s)
		if err != nil {
			return partialSpan{}, fmt.Errorf("%q is not a valid year-month", s)
		}
		return partialSpan{
			earliest: t,
			latest:   t.AddDate(0, 1, 0).Add(-time.Nanosecond),
			layout:   PrecisionMonth,
		}, nil

	case 10:
		t, err := time.Parse("2006-01-02", s)
		if err != nil {
			return partialSpan{}, fmt.Errorf("%q is not a valid date", s)
		}
		return partialSpan{
			earliest: t,
			latest:   t.AddDate(0, 0, 1).Add(-time.Nanosecond),
			layout:   PrecisionDay,
		}, nil
	}

	return partialSpan{}, fmt.Errorf("%q must be YYYY, YYYY-MM or YYYY-MM-DD", s)
}

// midpoint is why ordering is defined at all.
//
// The client currently sorts a range by its Start, which puts a photograph
// dated "1940-1990" before one dated "1945" - technically defensible, but it
// reads as though we know the broad one is older when we know no such thing.
// Sorting every kind by the middle of its span treats a wide range as what it
// is: an assertion centred somewhere, with error bars.
//
// This is a visible change to timeline and gallery ordering compared to the
// current client-side sort.
func midpoint(earliest, latest time.Time) time.Time {
	return earliest.Add(latest.Sub(earliest) / 2)
}

// formatInstant produces a fixed-width UTC RFC3339 string. Fixed width
// matters: these are concatenated into the index sort key, where a shorter
// string would sort in the wrong place.
func formatInstant(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000000000Z")
}

// precisionFor labels a span with the nearest order of magnitude.
//
// The boundaries are geometric means of the neighbouring labels rather than
// the labels themselves - the threshold between "year" and "decade" sits at
// sqrt(1 * 10) years, not at 1 year. Using the labels directly leaves gaps
// that read as nonsense: an eleven-year range is plainly decade-ish, but a
// "decade means at most ten years" rule would call it a century.
func precisionFor(earliest, latest time.Time) Precision {
	switch days := latest.Sub(earliest).Hours() / 24; {
	case days <= 5.6: // sqrt(1 * 31)
		return PrecisionDay
	case days <= 106: // sqrt(31 * 365)
		return PrecisionMonth
	case days <= 1154: // sqrt(365 * 3653)
		return PrecisionYear
	case days <= 11544: // sqrt(3653 * 36524)
		return PrecisionDecade
	default:
		return PrecisionCentury
	}
}

// bandTierFor buckets a span into the widths the timeline knows how to draw.
// Computing it here means a range and a circa date of the same width get the
// same band, which the client's separate code paths did not guarantee.
func bandTierFor(span time.Duration) CircaUnit {
	switch days := span.Hours() / 24; {
	case days <= 31:
		return UnitMonth
	case days <= 93:
		return UnitThreeMonths
	case days <= 366:
		return UnitYear
	case days <= 1827:
		return UnitFiveYears
	case days <= 3653:
		return UnitDecade
	default:
		return UnitCentury
	}
}

// ResolvedUnit is how wide a circa date's uncertainty is, inferring it from
// the precision the writer used when they did not say.
//
// The inferred width is one step wider than what was written. That is the
// whole point of the word: "circa 1960" has to mean something looser than
// "1960", and since an exact bare year already spans the whole of 1960, an
// inferred width of one year would make circa say nothing at all. So a year
// becomes five years, a month becomes three, a day becomes a month - each
// one notch up the same ladder an explicit unit picks from.
//
// An explicit unit always wins; this only fills a gap.
func (d *ArchiveDate) ResolvedUnit() CircaUnit {
	if d.Unit != "" {
		return d.Unit
	}

	span, err := parsePartial(d.Date)
	if err != nil {
		return UnitFiveYears
	}

	switch span.layout {
	case PrecisionDay:
		return UnitMonth
	case PrecisionMonth:
		return UnitThreeMonths
	default:
		return UnitFiveYears
	}
}

// Resolve writes any inferred value back onto the date, so the stored record
// says exactly what it means rather than depending on today's inference rule.
//
// This matters more for an archive than it would elsewhere: if the rule were
// applied only at read time and later changed, every previously stored circa
// date would quietly come to mean something slightly different. Pinning the
// width at write time means a record entered in 2026 still says in 2050 what
// it said when someone wrote it down.
func (d *ArchiveDate) Resolve() {
	if d == nil || d.Kind != DateCirca {
		return
	}
	d.Unit = d.ResolvedUnit()
}
