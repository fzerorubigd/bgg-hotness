package main

import (
	"bytes"
	"encoding/xml"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Tests use the standard library testing package rather than a third-party assertion
// library, matching this repository's existing zero-test-dependency footprint (see
// docscheck/readme_test.go); the feature should not be the reason go.mod grows a test
// dependency tree. The navigable-link check below is deliberately stdlib net/url on the
// same rel="alternate" field a reader's "visit site" button reads, rather than a real
// feed-parser dependency — parsing has always passed with no link at all, so the bar is
// that the link is present and navigates, not that the XML is well-formed.

func sampleRows() [][]string {
	return [][]string{
		{"1", "174430", "12", "https://boardgamegeek.com/boardgame/174430/", "Gloomhaven"},
		{"2", "999999", "9", "https://boardgamegeek.com/boardgame/999999/", ""}, // dropped upstream: blank name
	}
}

// --- Digest path (monthly -days=30 / yearly -year), unchanged shape ------------------

// A re-dispatched digest run REPLACES its entry (same title => same id) rather than
// appending a duplicate. updated moves to the new generation time; published stays the
// period end. Uses "Yearly - <year>", one of the three production titles.
func TestUpdateFeedDigestReplacesStableTitle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feed.xml")

	pub := time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC)
	gen1 := time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC)
	if err := updateFeedDigest(path, "Yearly - 2026", pub, gen1, sampleRows()); err != nil {
		t.Fatalf("first updateFeedDigest: %v", err)
	}
	gen2 := time.Date(2027, 3, 5, 0, 0, 0, 0, time.UTC)
	if err := updateFeedDigest(path, "Yearly - 2026", pub, gen2, sampleRows()); err != nil {
		t.Fatalf("re-dispatch updateFeedDigest: %v", err)
	}

	feed := parseFeed(t, path)
	if len(feed.Entry) != 1 {
		t.Fatalf("re-dispatch of the same title should replace, not append: got %d entries", len(feed.Entry))
	}
	if got, want := feed.Entry[0].Updated, gen2.Format(time.RFC3339); got != want {
		t.Errorf("updated should move to the re-run so a reader re-surfaces it: got %q want %q", got, want)
	}
	if got, want := feed.Entry[0].Published, pub.Format(time.RFC3339); got != want {
		t.Errorf("published should stay the period end: got %q want %q", got, want)
	}
}

// A digest entry aggregates many games and must carry NO <link> element — a nil Link
// slice omits it, where a value-typed field would emit <link href=""> (the #181 defect).
func TestDigestEntryHasNoLink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feed.xml")
	pub := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	gen := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
	if err := updateFeedDigest(path, "2026-08-01_30-days", pub, gen, sampleRows()); err != nil {
		t.Fatalf("updateFeedDigest: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read feed: %v", err)
	}
	if strings.Contains(string(raw), "<link") {
		t.Errorf("a digest entry must emit no <link> element; feed was:\n%s", raw)
	}
}

// --- Per-game path (weekly, -per-game) ----------------------------------------------

func perGameRows() [][]string {
	return [][]string{
		{"1", "174430", "12", "https://boardgamegeek.com/boardgame/174430/", "Gloomhaven"},
		{"2", "266192", "9", "https://boardgamegeek.com/boardgame/266192/", "Wingspan"},
	}
}

// The core of the change: one entry per game, each keyed on its numeric BGG id and each
// carrying a real rel="alternate" <link> to its own BGG page that actually navigates.
func TestPerGameOneEntryPerGameWithNavigableLink(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feed.xml")
	gen := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	if err := updateFeedPerGame(path, gen, perGameRows()); err != nil {
		t.Fatalf("updateFeedPerGame: %v", err)
	}

	feed := parseFeed(t, path)
	if len(feed.Entry) != 2 {
		t.Fatalf("two game rows should produce two entries, got %d", len(feed.Entry))
	}
	for _, want := range []string{
		tagPrefix + gamePrefix + "174430",
		tagPrefix + gamePrefix + "266192",
	} {
		e := findEntry(feed, want)
		if e == nil {
			t.Fatalf("expected an entry with id %q; ids were %v", want, entryIDs(feed))
		}
		id := strings.TrimPrefix(want, tagPrefix+gamePrefix)
		assertNavigable(t, e, id)
	}
}

// assertNavigable is the acceptance bar as a unit check: the entry carries exactly one
// alternate link whose href parses as an absolute http(s) URL on boardgamegeek.com
// pointing at this game — the field a reader's "visit site" actually follows.
func assertNavigable(t *testing.T, e *atomEntry, id string) {
	t.Helper()
	var alt *atomLink
	for i := range e.Link {
		if e.Link[i].Rel == "alternate" {
			alt = &e.Link[i]
			break
		}
	}
	if alt == nil {
		t.Fatalf("entry %q has no rel=alternate link (links: %+v)", e.ID, e.Link)
	}
	u, err := url.Parse(alt.Href)
	if err != nil {
		t.Fatalf("entry %q link href %q does not parse as a URL: %v", e.ID, alt.Href, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		t.Errorf("entry %q link is not navigable (scheme %q), href %q", e.ID, u.Scheme, alt.Href)
	}
	if u.Host != "boardgamegeek.com" {
		t.Errorf("entry %q link host = %q, want boardgamegeek.com", e.ID, u.Host)
	}
	if !strings.Contains(u.Path, id) {
		t.Errorf("entry %q link path %q does not reference the game id %q", e.ID, u.Path, id)
	}
}

// A game whose name is missing this run but that has no prior entry falls back to
// "BGG #<id>" so the entry stays legible; it still gets a navigable link.
func TestPerGameBlankNameFallsBackToID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feed.xml")
	gen := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	rows := [][]string{{"1", "999999", "3", "https://boardgamegeek.com/boardgame/999999/", ""}}
	if err := updateFeedPerGame(path, gen, rows); err != nil {
		t.Fatalf("updateFeedPerGame: %v", err)
	}
	feed := parseFeed(t, path)
	e := findEntry(feed, tagPrefix+gamePrefix+"999999")
	if e == nil {
		t.Fatalf("missing entry for the blank-name game; ids were %v", entryIDs(feed))
	}
	if e.Title != "BGG #999999" {
		t.Errorf("blank name on a new game should fall back to the id: got %q", e.Title)
	}
	assertNavigable(t, e, "999999")
}

// Update-in-place: the same game across two runs is ONE entry. published (first-seen) is
// preserved; a real content change (wins moved) advances updated.
func TestPerGameUpdateInPlacePreservesPublished(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feed.xml")
	gen1 := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)
	gen2 := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)

	if err := updateFeedPerGame(path, gen1, [][]string{
		{"1", "174430", "12", "https://boardgamegeek.com/boardgame/174430/", "Gloomhaven"},
	}); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if err := updateFeedPerGame(path, gen2, [][]string{
		{"1", "174430", "20", "https://boardgamegeek.com/boardgame/174430/", "Gloomhaven"},
	}); err != nil {
		t.Fatalf("run 2: %v", err)
	}

	feed := parseFeed(t, path)
	if len(feed.Entry) != 1 {
		t.Fatalf("the same game across runs should be one entry, got %d", len(feed.Entry))
	}
	e := feed.Entry[0]
	if got, want := e.Published, gen1.Format(time.RFC3339); got != want {
		t.Errorf("published should be preserved as first-seen: got %q want %q", got, want)
	}
	if got, want := e.Updated, gen2.Format(time.RFC3339); got != want {
		t.Errorf("a real content change should advance updated: got %q want %q", got, want)
	}
	if !strings.Contains(e.Content.Text, "20 wins") {
		t.Errorf("content should reflect the new run: got %q", e.Content.Text)
	}
}

// Condition 6/6a: when the rendered content is unchanged, updated is carried forward
// even though generation time advanced — so a re-run that changes nothing is byte-for-
// byte identical and the publish step's no-op guard skips the commit. This is the exact
// case a whole-struct compare (which includes the just-set updated) would silently fail.
func TestPerGameUnchangedRunIsByteIdenticalNoOp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feed.xml")
	gen1 := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)
	gen2 := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC) // later, but identical rows

	if err := updateFeedPerGame(path, gen1, perGameRows()); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after run 1: %v", err)
	}
	if err := updateFeedPerGame(path, gen2, perGameRows()); err != nil {
		t.Fatalf("run 2: %v", err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after run 2: %v", err)
	}
	if !bytes.Equal(first, second) {
		t.Errorf("an unchanged re-run must be byte-identical (no-op guard); diff:\nrun1:\n%s\nrun2:\n%s", first, second)
	}
}

// Sticky title: a transient blank name on an ESTABLISHED entry keeps the prior title
// rather than flapping to "BGG #<id>", so an upstream fetch hiccup does not advance
// updated and re-notify every subscriber.
func TestPerGameStickyTitleOnTransientBlankName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feed.xml")
	gen1 := time.Date(2026, 8, 11, 9, 0, 0, 0, time.UTC)
	gen2 := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)

	if err := updateFeedPerGame(path, gen1, [][]string{
		{"1", "266192", "9", "https://boardgamegeek.com/boardgame/266192/", "Wingspan"},
	}); err != nil {
		t.Fatalf("run 1: %v", err)
	}
	// Same rank+wins+link, but the name came back blank this run (transient miss).
	if err := updateFeedPerGame(path, gen2, [][]string{
		{"1", "266192", "9", "https://boardgamegeek.com/boardgame/266192/", ""},
	}); err != nil {
		t.Fatalf("run 2: %v", err)
	}

	feed := parseFeed(t, path)
	e := feed.Entry[0]
	if e.Title != "Wingspan" {
		t.Errorf("an established title should stick through a transient blank name: got %q", e.Title)
	}
	if got, want := e.Updated, gen1.Format(time.RFC3339); got != want {
		t.Errorf("a title flap from a fetch miss must not advance updated: got %q want %q", got, want)
	}
}

// Two rows sharing an id in ONE run must produce a single entry, not append a second
// with a duplicate id. byID is seeded from the on-disk entries, so the guard registers
// each appended entry's index for the rest of the loop; the later row updates the first
// in place. (Current inputs can't produce this — ids come from the ranking's own key
// set — so the guard makes the invariant local rather than resting on that property.)
func TestPerGameDuplicateIDInOneRunUpdatesNotAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feed.xml")
	gen := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	rows := [][]string{
		{"1", "174430", "12", "https://boardgamegeek.com/boardgame/174430/", "Gloomhaven"},
		{"5", "174430", "3", "https://boardgamegeek.com/boardgame/174430/", "Gloomhaven"}, // same id, later in the run
	}
	if err := updateFeedPerGame(path, gen, rows); err != nil {
		t.Fatalf("updateFeedPerGame: %v", err)
	}
	feed := parseFeed(t, path)
	if len(feed.Entry) != 1 {
		t.Fatalf("a duplicate id in one run must not append a second entry: got %d (%v)", len(feed.Entry), entryIDs(feed))
	}
	if !strings.Contains(feed.Entry[0].Content.Text, "3 wins") {
		t.Errorf("the later row should have updated the entry in place: got %q", feed.Entry[0].Content.Text)
	}
}

// --- Ordering (finalizeFeed) --------------------------------------------------------

// The comparator: newest-first by updated, ties broken by current-run rank, with a
// MISSING rank sorting LAST (not first, and not "incomparable"). This builds the exact
// triple that a happy-path test never constructs — an unranked entry sharing an instant
// with two ranked ones — which is where both the map-miss-zero bug (unranked jumps to
// the top) and the intransitive-gate bug (unspecified permutation) would show.
func TestFinalizeSortsByUpdatedThenRankUnrankedLast(t *testing.T) {
	newer := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC).Format(time.RFC3339)
	older := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)

	// Deliberately arrange the slice so that neither insertion order nor the map-miss
	// zero value would produce the correct answer by accident: the unranked entry is
	// placed first, and the ranked entries are out of rank order.
	feed := atomFeed{Entry: []atomEntry{
		{ID: "u", Updated: newer},   // unranked, in the newer tie group
		{ID: "r2", Updated: newer},  // rank 2
		{ID: "r1", Updated: newer},  // rank 1
		{ID: "old", Updated: older}, // older instant, ranked but not in this cohort
	}}
	rankByID := map[string]int{"r1": 1, "r2": 2} // "u" and "old" absent => +infinity

	finalizeFeed(&feed, rankByID, time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC))

	got := entryIDs(feed)
	want := []string{"r1", "r2", "u", "old"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v (updated DESC, then rank ASC, missing rank last)", got, want)
	}
	if feed.Updated != newer {
		t.Errorf("feed.Updated should be the max entry updated: got %q want %q", feed.Updated, newer)
	}
}

// An empty feed has no entry instant to borrow, so feed.Updated falls back to gen time.
func TestFinalizeEmptyFeedUpdatedFallsBackToNow(t *testing.T) {
	now := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	feed := atomFeed{}
	finalizeFeed(&feed, nil, now)
	if got, want := feed.Updated, now.Format(time.RFC3339); got != want {
		t.Errorf("empty feed updated = %q, want gen time %q", got, want)
	}
}

// Decision 2: per-game entries and a digest entry coexist in the same feed, and neither
// path disturbs the other's entries. Per-game ids (game:<id>) never collide with a
// digest id (slug of the title).
func TestPerGameAndDigestCoexist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "feed.xml")
	genW := time.Date(2026, 8, 18, 9, 0, 0, 0, time.UTC)
	if err := updateFeedPerGame(path, genW, perGameRows()); err != nil {
		t.Fatalf("weekly per-game: %v", err)
	}
	pubY := time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC)
	genY := time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC)
	if err := updateFeedDigest(path, "Yearly - 2026", pubY, genY, sampleRows()); err != nil {
		t.Fatalf("yearly digest: %v", err)
	}

	feed := parseFeed(t, path)
	if len(feed.Entry) != 3 {
		t.Fatalf("two per-game entries + one digest should coexist, got %d: %v", len(feed.Entry), entryIDs(feed))
	}
	for _, want := range []string{
		tagPrefix + gamePrefix + "174430",
		tagPrefix + gamePrefix + "266192",
		tagPrefix + slug("Yearly - 2026"),
	} {
		if findEntry(feed, want) == nil {
			t.Errorf("expected entry %q; ids were %v", want, entryIDs(feed))
		}
	}
	// The digest carries no link; the per-game entries do.
	if d := findEntry(feed, tagPrefix+slug("Yearly - 2026")); d != nil && len(d.Link) != 0 {
		t.Errorf("digest entry should carry no link, got %+v", d.Link)
	}
	if g := findEntry(feed, tagPrefix+gamePrefix+"174430"); g != nil && len(g.Link) == 0 {
		t.Errorf("per-game entry should carry a link")
	}
}

// --- Unchanged helpers / renderers --------------------------------------------------

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

	rows := [][]string{{"1", "5", "3", "https://boardgamegeek.com/boardgame/5/", "Tom & <b>Jerry</b>"}}
	if got := renderContent(rows); !strings.Contains(got, "Tom &amp; &lt;b&gt;Jerry&lt;/b&gt;") {
		t.Errorf("name with markup should be escaped; got %q", got)
	}
}

func TestRenderGameContentEscapesAndCarriesPayload(t *testing.T) {
	got := renderGameContent("3", "42")
	for _, want := range []string{"Rank 3", "42 wins"} {
		if !strings.Contains(got, want) {
			t.Errorf("per-game content missing %q; got %q", want, got)
		}
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"2026-08-11_14-days": "2026-08-11-14-days",
		"2026-08-01_30-days": "2026-08-01-30-days",
		"Yearly - 2026":      "yearly-2026",
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

func findEntry(feed atomFeed, id string) *atomEntry {
	for i := range feed.Entry {
		if feed.Entry[i].ID == id {
			return &feed.Entry[i]
		}
	}
	return nil
}

func entryIDs(feed atomFeed) []string {
	ids := make([]string, len(feed.Entry))
	for i, e := range feed.Entry {
		ids[i] = e.ID
	}
	return ids
}
