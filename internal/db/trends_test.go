package db

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestParseTrendTerms(t *testing.T) {
	got, err := ParseTrendTerms([]string{
		" load bearing | load-bearing ",
		"seam",
		"seam | Seam | seams",
		"slic",
	})
	if err != nil {
		t.Fatalf("ParseTrendTerms: %v", err)
	}
	if got[0].Term != "load bearing" {
		t.Fatalf("term label = %q", got[0].Term)
	}
	if want := []string{"load bearing", "load-bearing"}; !slices.Equal(got[0].Variants, want) {
		t.Fatalf("variants = %#v, want %#v", got[0].Variants, want)
	}
	if want := []string{"seam", "seams"}; !slices.Equal(got[1].Matchers, want) {
		t.Fatalf("matchers = %#v, want %#v", got[1].Matchers, want)
	}
	if got[2].Term != "seam" {
		t.Fatalf("deduped term label = %q", got[2].Term)
	}
	if want := []string{"seam", "seams"}; !slices.Equal(got[2].Variants, want) {
		t.Fatalf("deduped variants = %#v, want %#v", got[2].Variants, want)
	}
	if want := []string{"slic", "slics", "slice", "slices", "sliced", "slicing"}; !slices.Equal(got[3].Matchers, want) {
		t.Fatalf("stem matchers = %#v, want %#v", got[3].Matchers, want)
	}
}

func TestParseTrendTermsValidation(t *testing.T) {
	t.Run("empty input", func(t *testing.T) {
		if _, err := ParseTrendTerms(nil); err == nil {
			t.Fatal("expected error")
		}
	})

	t.Run("empty rows dropped before limit", func(t *testing.T) {
		got, err := ParseTrendTerms([]string{"", "  ", "seam"})
		if err != nil {
			t.Fatalf("ParseTrendTerms: %v", err)
		}
		if len(got) != 1 || got[0].Term != "seam" {
			t.Fatalf("terms = %#v, want only seam", got)
		}
	})

	t.Run("more than 12 terms", func(t *testing.T) {
		values := make([]string, MaxTrendTerms+1)
		for i := range values {
			values[i] = "term"
		}
		_, err := ParseTrendTerms(values)
		if err == nil || !strings.Contains(err.Error(), "at most 12") {
			t.Fatalf("error = %v, want max terms", err)
		}
	})

	t.Run("more than 8 variants after dedupe", func(t *testing.T) {
		_, err := ParseTrendTerms([]string{"a|b|c|d|e|f|g|h|i"})
		if err == nil || !strings.Contains(err.Error(), "at most 8") {
			t.Fatalf("error = %v, want max variants", err)
		}
	})

	t.Run("variant limit after dedupe", func(t *testing.T) {
		got, err := ParseTrendTerms([]string{"a|A|b|c|d|e|f|g|h"})
		if err != nil {
			t.Fatalf("ParseTrendTerms: %v", err)
		}
		if len(got[0].Variants) != MaxTrendTermVariants {
			t.Fatalf("variant count = %d, want %d", len(got[0].Variants), MaxTrendTermVariants)
		}
	})
}

func TestCountTrendOccurrences(t *testing.T) {
	term := TrendTermInput{
		Term:     "seam",
		Variants: []string{"seam"},
		Matchers: []string{"seam", "seams"},
	}
	cases := []struct {
		name string
		text string
		want int
	}{
		{"case insensitive", "Seam seam SEAMS", 3},
		{"word boundary", "seamless seam seams", 2},
		{"overlap plural", "seams", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := countTrendOccurrences(tc.text, term); got != tc.want {
				t.Fatalf("count = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestCountTrendOccurrencesSilentEStem(t *testing.T) {
	terms, err := ParseTrendTerms([]string{"slic"})
	if err != nil {
		t.Fatalf("ParseTrendTerms: %v", err)
	}
	got := countTrendOccurrences(
		"slice slices sliced slicing slicer sliced-up",
		terms[0],
	)
	if got != 5 {
		t.Fatalf("count = %d, want 5", got)
	}
}

func TestCountTrendOccurrencesPhrases(t *testing.T) {
	term := TrendTermInput{
		Term:     "load bearing",
		Variants: []string{"load bearing", "load-bearing"},
		Matchers: []string{"load bearing", "load-bearing"},
	}
	got := countTrendOccurrences("Load bearing and load-bearing", term)
	if got != 2 {
		t.Fatalf("count = %d, want 2", got)
	}
}

func TestTrendBucketDate(t *testing.T) {
	loc := time.UTC
	cases := []struct {
		gran string
		ts   string
		want string
	}{
		{"day", "2024-06-05T12:00:00Z", "2024-06-05"},
		{"week", "2024-06-05T12:00:00Z", "2024-06-03"},
		{"month", "2024-06-05T12:00:00Z", "2024-06-01"},
	}
	for _, tc := range cases {
		parsed, _ := time.Parse(time.RFC3339, tc.ts)
		if got := trendBucketDate(parsed, loc, tc.gran); got != tc.want {
			t.Fatalf("%s got %s want %s", tc.gran, got, tc.want)
		}
	}
}

func TestGetTrendsTermsSQLite(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	start := "2024-06-01T09:00:00Z"
	insertSession(t, d, "s1", "proj-a", func(s *Session) {
		s.StartedAt = &start
		s.CreatedAt = start
		s.MessageCount = 3
		s.UserMessageCount = 2
	})
	insertMessages(t, d,
		Message{SessionID: "s1", Ordinal: 0, Role: "user", Content: "load bearing seam", Timestamp: "2024-06-01T09:00:00Z", ContentLength: 17},
		Message{SessionID: "s1", Ordinal: 1, Role: "assistant", Content: "load-bearing seams seam", Timestamp: "2024-06-08T09:00:00Z", ContentLength: 23},
		Message{SessionID: "s1", Ordinal: 2, Role: "user", Content: "seam system", Timestamp: "2024-06-08T09:00:00Z", ContentLength: 11, IsSystem: true},
	)
	terms, err := ParseTrendTerms([]string{"load bearing | load-bearing", "seam"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.GetTrendsTerms(ctx, AnalyticsFilter{
		From: "2024-06-01", To: "2024-06-09", Timezone: "UTC",
	}, terms, "week")
	if err != nil {
		t.Fatalf("GetTrendsTerms: %v", err)
	}
	if want := []string{"2024-05-27", "2024-06-03"}; !slices.Equal(trendBucketDates(got.Buckets), want) {
		t.Fatalf("bucket dates = %#v, want %#v", trendBucketDates(got.Buckets), want)
	}
	if want := []int{1, 1}; !slices.Equal(trendBucketMessageCounts(got.Buckets), want) {
		t.Fatalf("bucket message counts = %#v, want %#v", trendBucketMessageCounts(got.Buckets), want)
	}
	if got.MessageCount != 2 {
		t.Fatalf("message count = %d, want 2", got.MessageCount)
	}
	byTerm := trendSeriesByTerm(got.Series)
	if got := byTerm["load bearing"].Total; got != 2 {
		t.Fatalf("load bearing total = %d, want 2", got)
	}
	if got := byTerm["seam"].Total; got != 3 {
		t.Fatalf("seam total = %d, want 3", got)
	}
	if want := []int{1, 1}; !slices.Equal(trendPointCounts(byTerm["load bearing"].Points), want) {
		t.Fatalf("load bearing points = %#v, want %#v", trendPointCounts(byTerm["load bearing"].Points), want)
	}
	if want := []int{1, 2}; !slices.Equal(trendPointCounts(byTerm["seam"].Points), want) {
		t.Fatalf("seam points = %#v, want %#v", trendPointCounts(byTerm["seam"].Points), want)
	}
}

func TestGetTrendsTermsSQLiteProjectFilter(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	start := "2024-06-01T09:00:00Z"
	insertSession(t, d, "s1", "proj-a", func(s *Session) {
		s.StartedAt = &start
		s.CreatedAt = start
		s.MessageCount = 1
		s.UserMessageCount = 1
	})
	insertSession(t, d, "s2", "proj-b", func(s *Session) {
		s.StartedAt = &start
		s.CreatedAt = start
		s.MessageCount = 1
		s.UserMessageCount = 1
	})
	insertMessages(t, d,
		Message{SessionID: "s1", Ordinal: 0, Role: "user", Content: "seam", Timestamp: start, ContentLength: 4},
		Message{SessionID: "s2", Ordinal: 0, Role: "user", Content: "seam", Timestamp: start, ContentLength: 4},
	)
	terms, err := ParseTrendTerms([]string{"seam"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.GetTrendsTerms(ctx, AnalyticsFilter{
		From: "2024-06-01", To: "2024-06-01", Timezone: "UTC", Project: "proj-a",
	}, terms, "day")
	if err != nil {
		t.Fatalf("GetTrendsTerms: %v", err)
	}
	if got := trendSeriesByTerm(got.Series)["seam"].Total; got != 1 {
		t.Fatalf("project-filtered total = %d, want 1", got)
	}
}

func TestGetTrendsTermsSQLiteUsesMessageTimestampRange(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	start := "2024-05-01T09:00:00Z"
	created := "2024-05-01T08:00:00Z"
	insertSession(t, d, "s1", "proj-a", func(s *Session) {
		s.StartedAt = &start
		s.CreatedAt = created
		s.MessageCount = 1
		s.UserMessageCount = 1
	})
	insertMessages(t, d,
		Message{SessionID: "s1", Ordinal: 0, Role: "user", Content: "seam", Timestamp: "2024-06-05T09:00:00Z", ContentLength: 4},
	)
	terms, err := ParseTrendTerms([]string{"seam"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.GetTrendsTerms(ctx, AnalyticsFilter{
		From: "2024-06-05", To: "2024-06-05", Timezone: "UTC",
	}, terms, "day")
	if err != nil {
		t.Fatalf("GetTrendsTerms: %v", err)
	}
	if got := trendSeriesByTerm(got.Series)["seam"].Total; got != 1 {
		t.Fatalf("message timestamp total = %d, want 1", got)
	}
}

func TestGetTrendsTermsSQLiteTimestampFallback(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	start := "2024-06-05T09:00:00Z"
	created := "2024-06-04T08:00:00Z"
	insertSession(t, d, "s1", "proj-a", func(s *Session) {
		s.StartedAt = &start
		s.CreatedAt = created
		s.MessageCount = 1
		s.UserMessageCount = 1
	})
	insertMessages(t, d,
		Message{SessionID: "s1", Ordinal: 0, Role: "user", Content: "seam", Timestamp: "not-a-time", ContentLength: 4},
	)
	terms, err := ParseTrendTerms([]string{"seam"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.GetTrendsTerms(ctx, AnalyticsFilter{
		From: "2024-06-05", To: "2024-06-05", Timezone: "UTC",
	}, terms, "day")
	if err != nil {
		t.Fatalf("GetTrendsTerms: %v", err)
	}
	if got := trendSeriesByTerm(got.Series)["seam"].Total; got != 1 {
		t.Fatalf("fallback timestamp total = %d, want 1", got)
	}
}

func TestGetTrendsTermsSQLiteExcludesLegacySystemPrefixes(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	start := "2024-06-01T09:00:00Z"
	insertSession(t, d, "s1", "proj-a", func(s *Session) {
		s.StartedAt = &start
		s.CreatedAt = start
		s.MessageCount = 2
		s.UserMessageCount = 2
	})
	insertMessages(t, d,
		Message{SessionID: "s1", Ordinal: 0, Role: "user", Content: SystemMsgPrefixes[0] + " seam", Timestamp: start, ContentLength: 40},
		Message{SessionID: "s1", Ordinal: 1, Role: "user", Content: "seam", Timestamp: start, ContentLength: 4},
	)
	terms, err := ParseTrendTerms([]string{"seam"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := d.GetTrendsTerms(ctx, AnalyticsFilter{
		From: "2024-06-01", To: "2024-06-01", Timezone: "UTC",
	}, terms, "day")
	if err != nil {
		t.Fatalf("GetTrendsTerms: %v", err)
	}
	if got := trendSeriesByTerm(got.Series)["seam"].Total; got != 1 {
		t.Fatalf("system-prefix-filtered total = %d, want 1", got)
	}
}

func trendBucketDates(buckets []TrendBucket) []string {
	dates := make([]string, len(buckets))
	for i, bucket := range buckets {
		dates[i] = bucket.Date
	}
	return dates
}

func trendBucketMessageCounts(buckets []TrendBucket) []int {
	counts := make([]int, len(buckets))
	for i, bucket := range buckets {
		counts[i] = bucket.MessageCount
	}
	return counts
}

func trendSeriesByTerm(series []TrendSeries) map[string]TrendSeries {
	byTerm := make(map[string]TrendSeries, len(series))
	for _, entry := range series {
		byTerm[entry.Term] = entry
	}
	return byTerm
}

func trendPointCounts(points []TrendPoint) []int {
	counts := make([]int, len(points))
	for i, point := range points {
		counts[i] = point.Count
	}
	return counts
}
