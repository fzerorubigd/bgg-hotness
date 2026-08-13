package main

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeRawFeed writes a feed with the given entries directly, so tests can build
// pre-cap or deliberately-malformed states that updateFeed would never produce.
func writeRawFeed(t *testing.T, path string, entries []atomEntry) {
	t.Helper()
	f := atomFeed{Title: feedTitle, ID: feedID, Author: atomAuthor{Name: authorName}, Entry: entries}
	b, err := xml.MarshalIndent(&f, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append([]byte(xml.Header), b...), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Over feedCap distinct runs cap to the newest feedCap, sorted newest-first.
func TestUpdateFeedCapsByCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feed.xml")
	base := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	total := feedCap + 5
	for i := 0; i < total; i++ {
		pub := base.Add(time.Duration(i) * time.Hour) // strictly increasing
		if err := updateFeed(path, fmt.Sprintf("run-%04d", i), pub, pub, sampleRows()); err != nil {
			t.Fatal(err)
		}
	}

	feed := parseFeed(t, path)
	if len(feed.Entry) != feedCap {
		t.Fatalf("feed should cap at %d, got %d", feedCap, len(feed.Entry))
	}
	if got, want := feed.Entry[0].Title, fmt.Sprintf("run-%04d", total-1); got != want {
		t.Errorf("newest entry should be first: got %q want %q", got, want)
	}
	if got, want := feed.Entry[len(feed.Entry)-1].Title, fmt.Sprintf("run-%04d", total-feedCap); got != want {
		t.Errorf("oldest kept should be %q, got %q", want, got)
	}
}

// A backfilled old-period entry, added last, must sort BELOW a recent one rather
// than sitting at the top on its old published (the pre-sort insertion behaviour).
func TestUpdateFeedSortsByPublishedDescFixesBackfill(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feed.xml")
	gen := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	recentPub := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	oldPub := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := updateFeed(path, "2026-08-01_14-days", recentPub, gen, sampleRows()); err != nil {
		t.Fatal(err)
	}
	if err := updateFeed(path, "Yearly - 2024", oldPub, gen.Add(time.Hour), sampleRows()); err != nil {
		t.Fatal(err)
	}

	feed := parseFeed(t, path)
	if feed.Entry[0].Title != "2026-08-01_14-days" {
		t.Errorf("newest published should sort first even though added last; got %q", feed.Entry[0].Title)
	}
	if feed.Entry[1].Title != "Yearly - 2024" {
		t.Errorf("backfilled old period should sort below; got %q", feed.Entry[1].Title)
	}
}

// The exact-id replace must survive sorting: re-dispatching a title already present
// replaces its entry rather than duplicating it (the ⚠️ no-duplicate half).
func TestUpdateFeedReplaceNoDuplicateAfterSort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feed.xml")
	pubA := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	pubB := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)

	for _, s := range []struct {
		title string
		pub   time.Time
		gen   time.Time
	}{
		{"Yearly - 2025", pubA, pubA},
		{"2026-06-01_14-days", pubB, pubB},
		{"Yearly - 2025", pubA, pubA.Add(48 * time.Hour)}, // re-dispatch, later gen
	} {
		if err := updateFeed(path, s.title, s.pub, s.gen, sampleRows()); err != nil {
			t.Fatal(err)
		}
	}

	feed := parseFeed(t, path)
	if len(feed.Entry) != 2 {
		t.Fatalf("re-dispatch must replace, not duplicate: got %d entries", len(feed.Entry))
	}
	n := 0
	for _, e := range feed.Entry {
		if e.Title == "Yearly - 2025" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("exactly one %q entry expected, got %d", "Yearly - 2025", n)
	}
}

// Below the cap, re-dispatching a title not currently present adds it as a new entry
// and it survives. (At the cap the divergent behaviour is: it re-adds, sorts to the
// bottom on its old published, and is truncated in the same pass — a "feed unchanged"
// no-op. This fixture is intentionally below the cap.)
func TestUpdateFeedReAddBelowCapSurvives(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feed.xml")
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := updateFeed(path, "2026-02-01_14-days", base.Add(31*24*time.Hour), base, sampleRows()); err != nil {
		t.Fatal(err)
	}
	if err := updateFeed(path, "Yearly - 2025", base, base.Add(time.Hour), sampleRows()); err != nil {
		t.Fatal(err)
	}

	feed := parseFeed(t, path)
	if len(feed.Entry) != 2 {
		t.Fatalf("below the cap the re-added entry survives: got %d entries", len(feed.Entry))
	}
}

// Skip truncation PER RUN, not per entry: if any published in the set fails to
// parse, the cap is not enforced at all and nothing is deleted — a cap on a key you
// could not parse is deleting on a guess. The realistic trigger is a future
// writing-format change; here one existing entry is corrupt.
func TestUpdateFeedSkipsTruncationOnUnparseablePublished(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feed.xml")

	entries := make([]atomEntry, feedCap+1)
	for i := range entries {
		pub := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour).Format(time.RFC3339)
		id := fmt.Sprintf("run-%04d", i)
		entries[i] = atomEntry{
			Title: id, ID: tagPrefix + id, Published: pub, Updated: pub,
			Content: atomContent{Type: "html", Text: "<ol></ol>"},
		}
	}
	entries[0].Published = "not-a-timestamp" // the corrupt key
	writeRawFeed(t, path, entries)

	newPub := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := updateFeed(path, "run-new", newPub, newPub, sampleRows()); err != nil {
		t.Fatal(err)
	}

	feed := parseFeed(t, path)
	if len(feed.Entry) != feedCap+2 {
		t.Fatalf("cap must be skipped when any key is unparseable: got %d, want %d", len(feed.Entry), feedCap+2)
	}
	found := false
	for _, e := range feed.Entry {
		if e.Published == "not-a-timestamp" {
			found = true
		}
	}
	if !found {
		t.Error("the unparseable entry must be retained, not deleted on a guess")
	}
}

// Control: with every key parseable the same over-cap set DOES truncate — so the
// skip above is doing the work, not a broken cap.
func TestUpdateFeedTruncatesWhenAllParseable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feed.xml")

	entries := make([]atomEntry, feedCap+1)
	for i := range entries {
		pub := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour).Format(time.RFC3339)
		id := fmt.Sprintf("run-%04d", i)
		entries[i] = atomEntry{
			Title: id, ID: tagPrefix + id, Published: pub, Updated: pub,
			Content: atomContent{Type: "html", Text: "<ol></ol>"},
		}
	}
	writeRawFeed(t, path, entries)

	newPub := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := updateFeed(path, "run-new", newPub, newPub, sampleRows()); err != nil {
		t.Fatal(err)
	}

	feed := parseFeed(t, path)
	if len(feed.Entry) != feedCap {
		t.Fatalf("with all keys parseable the cap applies: got %d, want %d", len(feed.Entry), feedCap)
	}
}
