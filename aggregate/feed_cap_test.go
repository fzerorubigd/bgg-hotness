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
		if err := updateFeedDigest(path, fmt.Sprintf("run-%04d", i), pub, pub, sampleRows()); err != nil {
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

// The feed is ordered newest-first by UPDATED (last change), not published (first
// publication): a re-generated old period surfaces at the top. Here the old-period
// entry is generated LATER, so despite its far-older published it sorts above the
// recent one — the opposite of a published sort, which is the point of the change.
func TestUpdateFeedSortsByUpdatedDesc(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feed.xml")
	recentPub := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	oldPub := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	genEarly := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	genLate := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)

	if err := updateFeedDigest(path, "2026-08-01_14-days", recentPub, genEarly, sampleRows()); err != nil {
		t.Fatal(err)
	}
	if err := updateFeedDigest(path, "Yearly - 2024", oldPub, genLate, sampleRows()); err != nil {
		t.Fatal(err)
	}

	feed := parseFeed(t, path)
	if feed.Entry[0].Title != "Yearly - 2024" {
		t.Errorf("the more recently UPDATED entry should sort first regardless of published; got %q", feed.Entry[0].Title)
	}
	if feed.Entry[1].Title != "2026-08-01_14-days" {
		t.Errorf("the less recently updated entry should sort below; got %q", feed.Entry[1].Title)
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
		if err := updateFeedDigest(path, s.title, s.pub, s.gen, sampleRows()); err != nil {
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

	if err := updateFeedDigest(path, "2026-02-01_14-days", base.Add(31*24*time.Hour), base, sampleRows()); err != nil {
		t.Fatal(err)
	}
	if err := updateFeedDigest(path, "Yearly - 2025", base, base.Add(time.Hour), sampleRows()); err != nil {
		t.Fatal(err)
	}

	feed := parseFeed(t, path)
	if len(feed.Entry) != 2 {
		t.Fatalf("below the cap the re-added entry survives: got %d entries", len(feed.Entry))
	}
}

// Skip truncation PER RUN, not per entry: if any UPDATED in the set (the sort key)
// fails to parse, the cap is not enforced at all and nothing is deleted — a cap on a
// key you could not parse is deleting on a guess. The realistic trigger is a future
// writing-format change; here one existing entry is corrupt.
func TestUpdateFeedSkipsTruncationOnUnparseableUpdated(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feed.xml")

	entries := make([]atomEntry, feedCap+1)
	for i := range entries {
		ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Hour).Format(time.RFC3339)
		id := fmt.Sprintf("run-%04d", i)
		entries[i] = atomEntry{
			Title: id, ID: tagPrefix + id, Published: ts, Updated: ts,
			Content: atomContent{Type: "html", Text: "<ol></ol>"},
		}
	}
	entries[0].Updated = "not-a-timestamp" // the corrupt sort key
	writeRawFeed(t, path, entries)

	newTS := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := updateFeedDigest(path, "run-new", newTS, newTS, sampleRows()); err != nil {
		t.Fatal(err)
	}

	feed := parseFeed(t, path)
	if len(feed.Entry) != feedCap+2 {
		t.Fatalf("cap must be skipped when any key is unparseable: got %d, want %d", len(feed.Entry), feedCap+2)
	}
	found := false
	for _, e := range feed.Entry {
		if e.Updated == "not-a-timestamp" {
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
	if err := updateFeedDigest(path, "run-new", newPub, newPub, sampleRows()); err != nil {
		t.Fatal(err)
	}

	feed := parseFeed(t, path)
	if len(feed.Entry) != feedCap {
		t.Fatalf("with all keys parseable the cap applies: got %d, want %d", len(feed.Entry), feedCap)
	}
}
