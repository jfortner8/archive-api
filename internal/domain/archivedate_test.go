package domain

import (
	"sort"
	"strings"
	"testing"
	"time"
)

func TestValidateRejects(t *testing.T) {
	tests := []struct {
		name    string
		date    ArchiveDate
		wantErr string
	}{
		{
			// The case that motivated enforcing this at all: the old handler
			// accepted and persisted it, and the UI then rendered whatever
			// its fallback happened to be.
			name:    "unknown kind",
			date:    ArchiveDate{Kind: "whenever", Date: "1952"},
			wantErr: "date.kind",
		},
		{
			name:    "empty date object",
			date:    ArchiveDate{},
			wantErr: "date.kind",
		},
		{
			name:    "exact with no date",
			date:    ArchiveDate{Kind: DateExact},
			wantErr: "date.date is required",
		},
		{
			name:    "exact carrying range fields",
			date:    ArchiveDate{Kind: DateExact, Date: "1952", Start: "1940"},
			wantErr: "do not apply",
		},
		{
			name:    "range with start after end",
			date:    ArchiveDate{Kind: DateRange, Start: "1990", End: "1940"},
			wantErr: "after date.end",
		},
		{
			name:    "circa with no unit",
			date:    ArchiveDate{Kind: DateCirca, Date: "1952"},
			wantErr: "not a known circa unit",
		},
		{
			name:    "circa with a misspelled unit",
			date:    ArchiveDate{Kind: DateCirca, Date: "1952", Unit: "decades"},
			wantErr: "not a known circa unit",
		},
		{
			name:    "month 13",
			date:    ArchiveDate{Kind: DateExact, Date: "1952-13"},
			wantErr: "not a valid year-month",
		},
		{
			name:    "unsupported format",
			date:    ArchiveDate{Kind: DateExact, Date: "06/15/1952"},
			wantErr: "not a valid date",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.date.Validate()
			if err == nil {
				t.Fatal("expected an error, got none")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestNormalizeBounds(t *testing.T) {
	tests := []struct {
		name         string
		date         ArchiveDate
		wantEarliest string
		wantLatest   string
		wantPrec     Precision
		wantTier     CircaUnit
	}{
		{
			name:         "exact day",
			date:         ArchiveDate{Kind: DateExact, Date: "1952-06-15"},
			wantEarliest: "1952-06-15",
			wantLatest:   "1952-06-15",
			wantPrec:     PrecisionDay,
			wantTier:     UnitMonth,
		},
		{
			// A bare year is a year, not 1 January. This is the case the old
			// lexical sort got wrong.
			name:         "bare year spans the whole year",
			date:         ArchiveDate{Kind: DateExact, Date: "1952"},
			wantEarliest: "1952-01-01",
			wantLatest:   "1952-12-31",
			wantPrec:     PrecisionYear,
			wantTier:     UnitYear,
		},
		{
			name:         "year-month spans the month",
			date:         ArchiveDate{Kind: DateExact, Date: "1952-02"},
			wantEarliest: "1952-02-01",
			wantLatest:   "1952-02-29", // 1952 was a leap year
			wantPrec:     PrecisionMonth,
			wantTier:     UnitMonth,
		},
		{
			name:         "range covers start of first to end of last",
			date:         ArchiveDate{Kind: DateRange, Start: "1940", End: "1950"},
			wantEarliest: "1940-01-01",
			wantLatest:   "1950-12-31",
			wantPrec:     PrecisionDecade,
			wantTier:     UnitCentury,
		},
		{
			// Centred on mid-1952, not on New Year's Day - and "unit: year"
			// means the uncertainty is a year WIDE, so it reaches half a year
			// either side rather than a full year.
			name:         "circa year",
			date:         ArchiveDate{Kind: DateCirca, Date: "1952", Unit: UnitYear},
			wantEarliest: "1951-12-31",
			wantLatest:   "1952-12-31",
			wantPrec:     PrecisionYear,
			wantTier:     UnitYear,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.date.Normalize()
			if err != nil {
				t.Fatalf("Normalize: %v", err)
			}
			if !strings.HasPrefix(got.Earliest, tt.wantEarliest) {
				t.Errorf("earliest = %q, want it to start with %q", got.Earliest, tt.wantEarliest)
			}
			if !strings.HasPrefix(got.Latest, tt.wantLatest) {
				t.Errorf("latest = %q, want it to start with %q", got.Latest, tt.wantLatest)
			}
			if got.Precision != tt.wantPrec {
				t.Errorf("precision = %q, want %q", got.Precision, tt.wantPrec)
			}
			if got.BandTier != tt.wantTier {
				t.Errorf("bandTier = %q, want %q", got.BandTier, tt.wantTier)
			}
		})
	}
}

// TestSortKeyIsFixedWidth guards the property the index depends on: sort keys
// are concatenated into the GSI range key, so a short one would sort wrong.
func TestSortKeyIsFixedWidth(t *testing.T) {
	dates := []ArchiveDate{
		{Kind: DateExact, Date: "800"},
		{Kind: DateExact, Date: "1952-06-15"},
		{Kind: DateCirca, Date: "1900", Unit: UnitCentury},
		{Kind: DateRange, Start: "1940", End: "1990"},
	}

	width := -1
	for _, d := range dates {
		// A three-digit year isn't a supported partial, so skip what won't
		// validate - the point here is the width of what does.
		n, err := d.Normalize()
		if err != nil {
			continue
		}
		if width == -1 {
			width = len(n.Sort)
			continue
		}
		if len(n.Sort) != width {
			t.Fatalf("sort key widths differ: %d vs %d (%q)", width, len(n.Sort), n.Sort)
		}
	}
	if width == -1 {
		t.Fatal("no dates normalized")
	}
}

// TestSortOrderIsByMidpoint pins the deliberate behaviour change: every kind
// sorts by the middle of its span, so a wide range no longer claims to be as
// old as its earliest possible day.
func TestSortOrderIsByMidpoint(t *testing.T) {
	type entry struct {
		label string
		date  ArchiveDate
	}

	entries := []entry{
		{"broad range 1940-1990", ArchiveDate{Kind: DateRange, Start: "1940", End: "1990"}},
		{"exact 1945", ArchiveDate{Kind: DateExact, Date: "1945"}},
		{"circa 1980", ArchiveDate{Kind: DateCirca, Date: "1980", Unit: UnitYear}},
	}

	keys := map[string]string{}
	for _, e := range entries {
		n, err := e.date.Normalize()
		if err != nil {
			t.Fatalf("%s: %v", e.label, err)
		}
		keys[e.label] = n.Sort
	}

	labels := []string{"broad range 1940-1990", "exact 1945", "circa 1980"}
	sort.Slice(labels, func(i, j int) bool { return keys[labels[i]] < keys[labels[j]] })

	// 1945 first, then the range centred on 1965, then 1980 - rather than the
	// range leading because it happens to start in 1940.
	want := []string{"exact 1945", "broad range 1940-1990", "circa 1980"}
	for i := range want {
		if labels[i] != want[i] {
			t.Fatalf("order = %v, want %v", labels, want)
		}
	}
}

func TestOverlaps(t *testing.T) {
	mustNormalize := func(d ArchiveDate) Normalized {
		t.Helper()
		n, err := d.Normalize()
		if err != nil {
			t.Fatalf("Normalize: %v", err)
		}
		return n
	}

	year := func(y int) time.Time {
		return time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC)
	}

	tests := []struct {
		name string
		date ArchiveDate
		from time.Time
		to   time.Time
		want bool
	}{
		{
			// The case a containment test would get wrong: a 1930-1960 range
			// genuinely might be a 1940s photograph.
			name: "wide range overlaps a narrower window",
			date: ArchiveDate{Kind: DateRange, Start: "1930", End: "1960"},
			from: year(1940), to: year(1950),
			want: true,
		},
		{
			name: "range entirely before the window",
			date: ArchiveDate{Kind: DateRange, Start: "1900", End: "1920"},
			from: year(1940), to: year(1950),
			want: false,
		},
		{
			name: "exact year inside the window",
			date: ArchiveDate{Kind: DateExact, Date: "1945"},
			from: year(1940), to: year(1950),
			want: true,
		},
		{
			name: "open-ended from",
			date: ArchiveDate{Kind: DateExact, Date: "1899"},
			to:   year(1950),
			want: true,
		},
		{
			name: "open-ended to",
			date: ArchiveDate{Kind: DateExact, Date: "2001"},
			from: year(1950),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := mustNormalize(tt.date).Overlaps(tt.from, tt.to); got != tt.want {
				t.Errorf("Overlaps = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestWireShapeRoundTrips guards the contract with archives-ui, which reuses
// these types verbatim. If the discriminator or either enum changes spelling,
// the timeline's bands and the map's circles degrade silently.
func TestWireShapeRoundTrips(t *testing.T) {
	units := []CircaUnit{UnitMonth, UnitThreeMonths, UnitYear, UnitFiveYears, UnitDecade, UnitCentury}
	want := []string{"month", "3-months", "year", "5-years", "decade", "century"}

	for i, u := range units {
		if string(u) != want[i] {
			t.Errorf("unit %d = %q, want %q", i, u, want[i])
		}
		d := ArchiveDate{Kind: DateCirca, Date: "1952", Unit: u}
		if err := d.Validate(); err != nil {
			t.Errorf("unit %q rejected: %v", u, err)
		}
	}

	for i, k := range []DateKind{DateExact, DateRange, DateCirca} {
		if got, expect := string(k), []string{"exact", "range", "circa"}[i]; got != expect {
			t.Errorf("kind = %q, want %q", got, expect)
		}
	}
}
