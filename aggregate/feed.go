package main

import (
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// feedCap bounds a feed to its most recent entries by count. It is a nice-to-have — the
// entries are the product — so it is given up entirely for any run whose sort key could
// not be fully parsed (see finalizeFeed).
const feedCap = 200

// Atom feed output. The aggregate jobs render Atom feeds committed to a dedicated
// branch; a reader subscribes to a feed's raw URL. There are THREE separate feeds, one
// file each on the feed branch, so a run of one job cannot touch another's entries and
// each feed caps independently:
//
//   - Weekly (feed.xml, the -per-game job): one entry PER GAME, keyed on the game's
//     numeric BGG id, updated in place across runs, each carrying a real <link> to its
//     BGG page. A game hot for six weeks is one entry, not six. Sorted by `updated`
//     (freshness) so the longest-hot games stay visible rather than sinking under the cap.
//   - Monthly (feed-monthly.xml) and Yearly (feed-yearly.xml, the digest jobs): one
//     entry per run, keyed on the run title, an <ol> of ranked games in the body and no
//     <link>. Sorted by `published` (period end), so a re-generated old period keeps its
//     chronological place — the guarantee the earlier single feed documented and lost
//     when its sort key moved to `updated` for the (then-shared) per-game path.
//
// Which shape a run emits is policy, not derivable from the window length, so it is
// carried by an explicit -per-game flag (see main.go's router). Each feed's id is
// derived from its filename (see feedIDForPath) and its title comes from FEED_TITLE.
// Three feeds MUST NOT share an <id> — a reader may legitimately dedupe or merge feeds
// on it — which the per-filename derivation makes structural.
//
// Entry identity: a per-game entry id is tagPrefix + "game:" + <BGG id>; a digest entry
// id is tagPrefix + slug(title); a FEED-level id is tagPrefix + <filename>. All three
// share tagPrefix; per-game vs digest ENTRY ids cannot collide because slug() emits only
// [a-z0-9-] and never the ':' a per-game id carries. Feed ids sit at the <feed> level and
// entry ids at <entry>, so a feed id and an entry id do not collide by position either.

const (
	authorName = "bgg-hotness"
	// tagPrefix + a suffix is an id. A tag: URI is used rather than the raw feed URL
	// because it is stable regardless of where the feed is hosted, so a reader's dedupe
	// survives the feed being moved or mirrored — a property the FEED-level id gives up
	// by deriving from the filename (see feedIDForPath); entry ids keep it.
	tagPrefix = "tag:github.com,2024:bgg-hotness:"
	// gamePrefix namespaces a per-game entry id under tagPrefix. It contains a ':', which
	// slug() (digest entry ids) can never emit, so per-game and digest ENTRY ids are
	// collision-proof by construction. Feed-level ids also sit under tagPrefix but at a
	// different XML level; see feedIDForPath.
	gamePrefix = "game:"
	// defaultFeedTitle is the weekly feed's title and the fallback when FEED_TITLE is
	// unset. A wrong title is cosmetic (unlike a wrong id), so a default is safe here.
	defaultFeedTitle = "BGG Hotness Aggregates"
)

// feedIDForPath derives the feed-level Atom id from the feed FILE's basename:
// tagPrefix + basename-without-extension. feed.xml -> tag:github.com,2024:bgg-hotness:feed
// (byte-identical to the original single-feed id, so the live weekly subscription is
// untouched); feed-monthly.xml -> ...:feed-monthly; feed-yearly.xml -> ...:feed-yearly.
//
// ⚠️ The filename is LOAD-BEARING for feed identity. Two feeds cannot share a path, so
// distinct ids are structural rather than something a config author must remember to keep
// unique — but the flip side is that RENAMING a feed file re-identifies the feed to every
// subscriber. loadFeed rewrites feed.ID from this on every run, so a `git mv feed.xml
// feed-weekly.xml` (which looks like harmless tidying and touches no Go) would silently
// change the weekly feed's <id> on the next run. If you must rename feed.xml, PIN the old
// id explicitly in the same commit so the id does not move. TestFeedIDForPathWeeklyLiteral
// guards this by asserting the exact literal, so a rename becomes a failing test rather
// than a silent re-identification. Precondition: distinctness holds for basenames within
// ONE directory — same-named files in different trees derive the same id, because the
// derivation discards the directory that distinguishes them.
func feedIDForPath(path string) string {
	base := filepath.Base(path)
	return tagPrefix + strings.TrimSuffix(base, filepath.Ext(base))
}

type atomFeed struct {
	XMLName xml.Name    `xml:"http://www.w3.org/2005/Atom feed"`
	Title   string      `xml:"title"`
	ID      string      `xml:"id"`
	Updated string      `xml:"updated"`
	Author  atomAuthor  `xml:"author"`
	Entry   []atomEntry `xml:"entry"`
}

type atomAuthor struct {
	Name string `xml:"name"`
}

type atomEntry struct {
	Title     string `xml:"title"`
	ID        string `xml:"id"`
	Published string `xml:"published"`
	Updated   string `xml:"updated"`
	// Link is a SLICE, not a value struct: a nil slice marshals to no element, while a
	// value-typed field would always emit <link href=""> even on a digest entry that
	// has no game to point at (the empty-element defect from #181). Per-game entries
	// carry exactly one rel="alternate" link — the BGG page — which is the field a
	// reader's "visit site" navigates to; digest entries carry none.
	Link    []atomLink  `xml:"link"`
	Content atomContent `xml:"content"`
}

type atomLink struct {
	Rel  string `xml:"rel,attr,omitempty"`
	Href string `xml:"href,attr"`
}

type atomContent struct {
	Type string `xml:"type,attr"`
	Text string `xml:",chardata"`
}

// loadFeed reads the existing feed at path (treating an absent file as an empty feed)
// and reasserts the canonical feed-level fields so an older or hand-edited feed converges
// rather than preserving stale values. The feed id is derived from the path (see
// feedIDForPath); feedTitle is the feed-level title. feed.Updated is deliberately left
// unset here; finalizeFeed derives it from the entries once they are known.
func loadFeed(path, feedTitle string) (atomFeed, error) {
	feed := atomFeed{}
	if b, err := os.ReadFile(path); err == nil {
		if err := xml.Unmarshal(b, &feed); err != nil {
			return feed, fmt.Errorf("parse existing feed %q: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return feed, fmt.Errorf("read existing feed %q: %w", path, err)
	}
	// Clear XMLName so the struct tag (not the value parsed from disk) supplies the
	// Atom namespace on marshal.
	feed.XMLName = xml.Name{}
	feed.Title = feedTitle
	feed.ID = feedIDForPath(path)
	feed.Author = atomAuthor{Name: authorName}
	return feed, nil
}

// saveFeed marshals feed and writes it to path via a temp file + rename, so a crash
// mid-write cannot leave a truncated feed the next run would fail to parse.
func saveFeed(path string, feed atomFeed) error {
	out, err := xml.MarshalIndent(&feed, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal feed: %w", err)
	}
	out = append([]byte(xml.Header), append(out, '\n')...)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// updateFeedDigest inserts or replaces the single entry for this run and writes the
// result — the monthly (feed-monthly.xml) and yearly (feed-yearly.xml) jobs. feedTitle is
// the feed-level title; entryTitle is this run's title (the entry id keys off it).
// published is the end of the aggregated period; updated is generation time. Digest feeds
// sort by published (see finalizeFeed's sortByPublished). rows are the ranked game rows
// [rank, id, wins, link, name].
func updateFeedDigest(path, feedTitle, entryTitle string, published, updated time.Time, rows [][]string) error {
	feed, err := loadFeed(path, feedTitle)
	if err != nil {
		return err
	}

	entry := atomEntry{
		Title:     entryTitle,
		ID:        tagPrefix + slug(entryTitle),
		Published: published.UTC().Format(time.RFC3339),
		Updated:   updated.UTC().Format(time.RFC3339),
		Content:   atomContent{Type: "html", Text: renderContent(rows)},
		// Link intentionally nil: a digest aggregates many games and has no single
		// page to link to.
	}

	replaced := false
	for i := range feed.Entry {
		if feed.Entry[i].ID == entry.ID {
			// A digest entry is written once per period, so a wholesale replace is
			// correct: published is a constant for that period, not a first-seen
			// instant to preserve.
			feed.Entry[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		feed.Entry = append([]atomEntry{entry}, feed.Entry...)
	}

	// Digest feeds carry no per-game ranks (nil tie-break) and sort by published.
	finalizeFeed(&feed, nil, updated, true)
	return saveFeed(path, feed)
}

// updateFeedPerGame upserts one entry per game row (the weekly feed.xml job). Each game's
// entry is keyed on its numeric BGG id and updated in place across runs: published is the
// first-seen instant and is preserved, updated advances only when the rendered content
// actually changes, and every entry carries a rel="alternate" <link> to its BGG page.
// feedTitle is the feed-level title; now is this run's generation instant. rows are
// [rank, id, wins, link, name].
func updateFeedPerGame(path, feedTitle string, now time.Time, rows [][]string) error {
	feed, err := loadFeed(path, feedTitle)
	if err != nil {
		return err
	}
	nowStr := now.UTC().Format(time.RFC3339)

	byID := make(map[string]int, len(feed.Entry))
	for i := range feed.Entry {
		byID[feed.Entry[i].ID] = i
	}
	// rankByID is this run's rank for each entry id and is the ONLY source of rank.
	// It is never persisted to the feed, so a stale rank from an earlier cohort cannot
	// exist to be mis-compared (finalizeFeed relies on this).
	rankByID := make(map[string]int, len(rows))

	for _, r := range rows {
		if len(r) < 5 {
			continue
		}
		rank, id, wins, link, name := r[0], r[1], r[2], r[3], r[4]
		entryID := tagPrefix + gamePrefix + id
		if n, err := strconv.Atoi(rank); err == nil {
			rankByID[entryID] = n
		}
		idx, exists := byID[entryID]

		title := name
		if title == "" {
			if exists {
				// Sticky title: a blank name is a transient upstream miss (main.go
				// emits the row with the known id and a blank name rather than
				// panicking). Keep the established title so a Wingspan -> BGG #id ->
				// Wingspan flap from a fetch hiccup does not advance updated and
				// re-notify every subscriber. published/updated are already preserved
				// on no real change; this stops the title itself from manufacturing one.
				title = feed.Entry[idx].Title
			} else {
				// A genuinely new game whose name is missing this run: show the id
				// rather than an empty title so the entry stays legible.
				title = "BGG #" + id
			}
		}
		content := atomContent{Type: "html", Text: renderGameContent(rank, wins)}
		links := []atomLink{{Rel: "alternate", Href: link}}

		if exists {
			e := &feed.Entry[idx]
			// Condition 6/6a: advance updated ONLY on a real content change, and
			// compare only the fields being written EXCEPT updated itself. updated is
			// already set to now, so including it in the comparison would always differ
			// and make the carry-forward a silent no-op. published is preserved
			// untouched — a wholesale replace would reset first-seen to now every week,
			// pinning a long-hot game to the top and re-notifying readers each run.
			if e.Title != title || e.Content.Text != content.Text || !sameLinks(e.Link, links) {
				e.Updated = nowStr
			}
			e.Title = title
			e.Content = content
			e.Link = links
		} else {
			feed.Entry = append(feed.Entry, atomEntry{
				Title:     title,
				ID:        entryID,
				Published: nowStr,
				Updated:   nowStr,
				Link:      links,
				Content:   content,
			})
			// Register the new entry's index so a LATER row with the same id in THIS
			// run updates it in place rather than appending a second entry with a
			// duplicate id. byID was built from the entries already on disk, so without
			// this an in-run duplicate would orphan the second copy permanently. Today's
			// rows come from the ranking's own key set (distinct by construction), so
			// this makes the no-duplicate invariant local instead of resting on that
			// upstream property. (Which of two same-id rows wins is arbitrary and
			// unreached, not a chosen policy — see TestPerGameDuplicateIDInOneRun.)
			byID[entryID] = len(feed.Entry) - 1
		}
	}

	// Per-game feeds sort by updated (freshness); rankByID breaks ties within a run.
	finalizeFeed(&feed, rankByID, now, false)
	return saveFeed(path, feed)
}

// finalizeFeed orders entries newest-first, caps by count, and sets the feed-level
// updated.
//
// sortByPublished selects the PRIMARY sort key for the WHOLE feed — chosen once per feed,
// never per entry:
//   - false (weekly per-game): sort by `updated` (last change). Under update-in-place a
//     game hot in week 1 and still hot in week 6 keeps published=week-1, so a published
//     sort would sink the longest-hot games to the bottom, and the cap drops the bottom,
//     in a feed whose purpose is what is hot now.
//   - true (monthly/yearly digest): sort by `published` (period end). This RESTORES the
//     chronological-placement guarantee the earlier single feed documented and lost when
//     its sort key moved to `updated` for the (then-shared) per-game path — a re-generated
//     old period sits in its correct place rather than jumping to the top. The restoration
//     is deliberate, recorded here rather than reappearing silently.
//
// It is a whole-list choice because each feed file holds a single shape. A per-ENTRY
// variant (read published for a digest entry, updated for a per-game entry, in one list)
// is NOT a strict weak ordering — it goes intransitive across mixed shapes and sort then
// yields an unspecified permutation with nothing failing. The three-feed split exists
// partly so this choice never has to be per-entry.
//
// rankByID maps an entry id to its rank in THIS run (nil on the digest path). now is the
// fallback feed-updated for an empty feed.
func finalizeFeed(feed *atomFeed, rankByID map[string]int, now time.Time, sortByPublished bool) {
	type keyedEntry struct {
		entry atomEntry
		when  time.Time
	}
	keyed := make([]keyedEntry, len(feed.Entry))
	capSafe := true
	var maxUpdated time.Time
	for i, e := range feed.Entry {
		// Sort-key field for this feed. capSafe follows the SORT key — a parse failure of
		// the field the cap orders on makes truncation unsafe — while feed.Updated tracks
		// `updated` separately below. Parsed as an instant, not compared lexically, so a
		// later format or zone change cannot silently misorder.
		sortField, fieldName := e.Updated, "updated"
		if sortByPublished {
			sortField, fieldName = e.Published, "published"
		}
		when, err := time.Parse(time.RFC3339, sortField)
		if err != nil {
			// Non-fatal, matching the feed path's policy of never aborting the sheet
			// output. A key we could not parse makes the cap unsafe this run, so record
			// a reason instead of guessing.
			fmt.Fprintf(os.Stderr, "feed: unparseable %s %q on entry %q: %v\n", fieldName, sortField, e.ID, err)
			capSafe = false
		}
		keyed[i] = keyedEntry{entry: e, when: when}
		// Feed-level updated is always max(entry.updated), independent of the sort key.
		if u, uerr := time.Parse(time.RFC3339, e.Updated); uerr == nil && u.After(maxUpdated) {
			maxUpdated = u
		}
	}

	// Tie key: current-run rank ASCENDING (per-game only; rankByID is nil for digests,
	// where ties fall to stable order). A tie group shares one sort instant, so for the
	// per-game feed it is one run's cohort and the ranks are genuinely comparable. A
	// missing rank is +infinity, so unmapped entries sort last within a tie and tie among
	// themselves. +infinity, not "refuse to compare when either is unmapped": gating that
	// way makes the predicate non-transitive (A==B, C==B, yet C<A) — not a strict weak
	// ordering — and sort then yields an unspecified permutation. The sentinel keeps a
	// total order, and nothing reads a stale rank even if the sort key ever loses
	// resolution and cohorts merge, because an out-of-run entry is simply +infinity.
	rankOf := func(id string) int {
		if r, ok := rankByID[id]; ok {
			return r
		}
		return math.MaxInt
	}
	// SliceStable, not Slice: entries the comparator deems equal (same sort instant, same
	// rank sentinel) must keep insertion order. An unstable sort would permute them, and
	// the publish step's no-op guard is a byte-level `git diff --cached --quiet`, so the
	// permutation would manufacture a commit with no semantic change every run.
	sort.SliceStable(keyed, func(i, j int) bool {
		if !keyed[i].when.Equal(keyed[j].when) {
			return keyed[i].when.After(keyed[j].when)
		}
		return rankOf(keyed[i].entry.ID) < rankOf(keyed[j].entry.ID)
	})

	// Cap by count, but skip truncation entirely for any run where a key failed to
	// parse — the skip is per RUN, not per entry: dropping "only" the sorted tail still
	// deletes on an incomplete ordering. When skipped, the feed drifts over the cap
	// until the stderr line is acted on; nothing is lost.
	keep := len(keyed)
	if capSafe && keep > feedCap {
		keep = feedCap
	}
	feed.Entry = make([]atomEntry, keep)
	for i := 0; i < keep; i++ {
		feed.Entry[i] = keyed[i].entry
	}

	// Feed-level updated is the most recent entry updated, so a run that changes no entry
	// produces byte-identical output and the publish step's no-op guard skips the commit.
	// An empty feed has no entry instant to borrow, so it falls back to generation time.
	if maxUpdated.IsZero() {
		feed.Updated = now.UTC().Format(time.RFC3339)
	} else {
		feed.Updated = maxUpdated.UTC().Format(time.RFC3339)
	}
}

// sameLinks reports whether two link slices are element-wise equal. It is the change
// detector for the per-game Link field (condition 6): a differing link is a real change
// that must advance updated.
func sameLinks(a, b []atomLink) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// renderContent builds a DIGEST entry body as an ordered list of ranked games, each a
// BGG link. It is emitted as Atom content type="html"; encoding/xml escapes the whole
// string once as chardata, so the inner HTML is additionally html-escaped here to stay
// well-formed after a reader un-escapes it.
func renderContent(rows [][]string) string {
	var b strings.Builder
	b.WriteString("<ol>")
	for _, r := range rows {
		if len(r) < 5 {
			continue
		}
		name := r[4]
		if name == "" {
			// BGG dropped this id upstream (retired/invalid); show the id rather than
			// an empty link so the entry stays legible.
			name = "BGG #" + r[1]
		}
		fmt.Fprintf(&b, "<li><a href=\"%s\">%s</a> — %s wins</li>",
			html.EscapeString(r[3]), html.EscapeString(name), html.EscapeString(r[2]))
	}
	b.WriteString("</ol>")
	return b.String()
}

// renderGameContent is the PER-GAME entry body: the rank and wins for this run, which
// are the payload. It is kept free of any run-specific text (dates, "this week") so an
// unchanged game compares byte-equal against the prior run and does not advance updated
// or re-notify (condition 6). The navigable BGG link is the entry's <link>, not the
// body, so the body needs no anchor.
func renderGameContent(rank, wins string) string {
	return fmt.Sprintf("<p>Rank %s in the latest BGG Hotness aggregate (%s wins).</p>",
		html.EscapeString(rank), html.EscapeString(wins))
}

// slug reduces a run title to a stable, URI-safe fragment for a digest entry id:
// lowercase, with each run of non-alphanumeric characters collapsed to a single dash.
// The same title always yields the same slug, which gives a re-dispatched digest run
// its replace-not-append behaviour. It emits only [a-z0-9-], so a digest id can never
// contain the ':' that a per-game id carries.
func slug(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		case !prevDash:
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
