package main

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Tests use the standard library testing package rather than a third-party
// assertion library, matching this repository's existing zero-test-dependency
// footprint (see docscheck/readme_test.go); the feature should not be the reason
// go.mod grows a test dependency tree.

func sampleRows() [][]string {
	return [][]string{
		{"1", "174430", "12", "https://boardgamegeek.com/boardgame/174430/", "Gloomhaven"},
		{"2", "999999", "9", "https://boardgamegeek.com/boardgame/999999/", ""}, // dropped upstream: blank name
	}
}

// A fresh feed and then a second run append newest-first, each entry carrying the
// period end as published and generation time as updated.
func TestUpdateFeedAppendsNewestFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feed.xml")

	pub1 := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	gen1 := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)
	if err := updateFeed(path, "2026-08-11_14-days", pub1, gen1, sampleRows()); err != nil {
		t.Fatalf("first updateFeed: %v", err)
	}

	pub2 := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	gen2 := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	if err := updateFeed(path, "2026-08-18_14-days", pub2, gen2, sampleRows()); err != nil {
		t.Fatalf("second updateFeed: %v", err)
	}

	feed := parseFeed(t, path)
	if len(feed.Entry) != 2 {
		t.Fatalf("two distinct-titled runs should produce two entries, got %d", len(feed.Entry))
	}
	if feed.Entry[0].Title != "2026-08-18_14-days" {
		t.Errorf("newest run should be first, got %q", feed.Entry[0].Title)
	}
	if feed.Entry[1].Title != "2026-08-11_14-days" {
		t.Errorf("older run should be second, got %q", feed.Entry[1].Title)
	}
	if got, want := feed.Entry[0].Published, pub2.Format(time.RFC3339); got != want {
		t.Errorf("published should be the period end: got %q want %q", got, want)
	}
	if got, want := feed.Entry[0].Updated, gen2.Format(time.RFC3339); got != want {
		t.Errorf("updated should be generation time: got %q want %q", got, want)
	}
	if got, want := feed.Updated, gen2.Format(time.RFC3339); got != want {
		t.Errorf("feed-level updated should track the latest run: got %q want %q", got, want)
	}
	if feed.ID != feedID {
		t.Errorf("feed id: got %q want %q", feed.ID, feedID)
	}
	if feed.Author.Name != authorName {
		t.Errorf("author: got %q want %q", feed.Author.Name, authorName)
	}
}

// The yearly job's title carries no date, so a re-dispatch must REPLACE its entry
// (same id) rather than append a duplicate — the identity that keeps a re-run from
// piling up. updated moves to the new generation time; published stays the period end.
func TestUpdateFeedReplacesStableTitle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feed.xml")

	pub := time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC)
	gen1 := time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC)
	if err := updateFeed(path, "Yearly - 2026", pub, gen1, sampleRows()); err != nil {
		t.Fatalf("first updateFeed: %v", err)
	}
	// Re-dispatch of the same year, later generation time.
	gen2 := time.Date(2027, 3, 5, 0, 0, 0, 0, time.UTC)
	if err := updateFeed(path, "Yearly - 2026", pub, gen2, sampleRows()); err != nil {
		t.Fatalf("re-dispatch updateFeed: %v", err)
	}

	feed := parseFeed(t, path)
	if len(feed.Entry) != 1 {
		t.Fatalf("re-dispatch of the same year should replace, not append: got %d entries", len(feed.Entry))
	}
	if got, want := feed.Entry[0].Updated, gen2.Format(time.RFC3339); got != want {
		t.Errorf("updated should move to the re-run so a reader re-surfaces it: got %q want %q", got, want)
	}
	if got, want := feed.Entry[0].Published, pub.Format(time.RFC3339); got != want {
		t.Errorf("published should stay the period end: got %q want %q", got, want)
	}
}

// A date-bearing entry and a stable-title entry coexist; re-running only the stable
// one leaves the date-bearing entry untouched.
func TestUpdateFeedMixedIdentitiesPreserved(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feed.xml")
	base := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)

	for _, step := range []struct {
		title string
		gen   time.Time
	}{
		{"2026-08-11_14-days", base},
		{"Yearly - 2026", base},
		{"Yearly - 2026", base.Add(48 * time.Hour)},
	} {
		if err := updateFeed(path, step.title, base, step.gen, sampleRows()); err != nil {
			t.Fatalf("updateFeed(%q): %v", step.title, err)
		}
	}

	feed := parseFeed(t, path)
	if len(feed.Entry) != 2 {
		t.Fatalf("the days-based entry should survive the yearly re-run: got %d entries", len(feed.Entry))
	}
	titles := feed.Entry[0].Title + "|" + feed.Entry[1].Title
	for _, want := range []string{"2026-08-11_14-days", "Yearly - 2026"} {
		if !strings.Contains(titles, want) {
			t.Errorf("expected entry %q to be present, titles were %q", want, titles)
		}
	}
}

func TestRenderContent(t *testing.T) {
	body := renderContent(sampleRows())
	for _, want := range []string{
		`<a href="https://boardgamegeek.com/boardgame/174430/">Gloomhaven</a>`,
		"12 wins",
		"BGG #999999", // a blank name falls back to the id rather than an empty link
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered content missing %q; got %q", want, body)
		}
	}

	// A name with markup characters must not break the emitted HTML.
	rows := [][]string{{"1", "5", "3", "https://boardgamegeek.com/boardgame/5/", "Tom & <b>Jerry</b>"}}
	if got := renderContent(rows); !strings.Contains(got, "Tom &amp; &lt;b&gt;Jerry&lt;/b&gt;") {
		t.Errorf("name with markup should be escaped; got %q", got)
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"2026-08-11_14-days": "2026-08-11-14-days",
		"Yearly - 2026":      "yearly-2026",
		"Monthly - 2026-8":   "monthly-2026-8",
	}
	for in, want := range cases {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func parseFeed(t *testing.T, path string) atomFeed {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read feed: %v", err)
	}
	var feed atomFeed
	if err := xml.Unmarshal(b, &feed); err != nil {
		t.Fatalf("the written feed must be parseable XML: %v", err)
	}
	return feed
}
