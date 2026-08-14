package main

import (
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// feedCap bounds the feed to its most recent entries by count. It is a
// nice-to-have — the entries are the product — so it is given up entirely for any
// run whose sort key could not be fully parsed (see finalizeFeed).
const feedCap = 200

// Atom feed output. The aggregate job renders an Atom feed committed to a dedicated
// branch; a reader (my-rss) subscribes to the raw URL. There are two entry shapes:
//
//   - Per-game (the weekly job, opt-in via -per-game): one entry PER GAME, keyed on
//     the game's numeric BGG id, updated in place across runs, each carrying a real
//     <link> to its own BGG page. A game hot for six weeks is one entry, not six.
//   - Digest (every other invocation): one entry for the whole run, keyed on the run
//     title, an <ol> of ranked games in the body and no <link>. This is the original
//     shape and the monthly (-days=30) and yearly (-year) jobs keep it unchanged.
//
// Which shape a run emits is policy, not something derivable from the window length,
// so it is carried by an explicit flag rather than inferred (see main.go's router).
//
// Entry identity: a per-game id is tagPrefix + "game:" + <BGG id>; a digest id is
// tagPrefix + slug(title). The two id spaces cannot collide because slug() emits only
// [a-z0-9-] and so never produces the ':' that every per-game id contains.

const (
	feedTitle  = "BGG Hotness Aggregates"
	feedID     = "tag:github.com,2024:bgg-hotness:feed"
	authorName = "bgg-hotness"
	// tagPrefix + the per-run or per-game suffix is the entry id. A tag: URI is used
	// rather than the raw feed URL because it is stable regardless of where the feed
	// is hosted, so a reader's dedupe survives the feed being moved or mirrored.
	tagPrefix = "tag:github.com,2024:bgg-hotness:"
	// gamePrefix namespaces a per-game entry id under tagPrefix. It contains a ':',
	// which slug() (used for digest ids) can never emit, so per-game and digest ids
	// are collision-proof by construction.
	gamePrefix = "game:"
)

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
// and reasserts the canonical feed-level fields so an older or hand-edited feed
// converges rather than preserving stale values. feed.Updated is deliberately left
// unset here; finalizeFeed derives it from the entries once they are known.
func loadFeed(path string) (atomFeed, error) {
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
	feed.ID = feedID
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
// result. This is the original one-entry-per-run shape, kept unchanged for the
// monthly (-days=30) and yearly (-year) jobs. published is the end of the aggregated
// period; updated is generation time. rows are the ranked game rows
// [rank, id, wins, link, name].
func updateFeedDigest(path, title string, published, updated time.Time, rows [][]string) error {
	feed, err := loadFeed(path)
	if err != nil {
		return err
	}

	entry := atomEntry{
		Title:     title,
		ID:        tagPrefix + slug(title),
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

	// The digest path carries no per-game ranks, so the tie-break has nothing to key
	// on and falls through to stable order (see finalizeFeed).
	finalizeFeed(&feed, nil, updated)
	return saveFeed(path, feed)
}

// updateFeedPerGame upserts one entry per game row (the weekly job). Each game's entry
// is keyed on its numeric BGG id and updated in place across runs: published is the
// first-seen instant and is preserved, updated advances only when the rendered content
// actually changes, and every entry carries a rel="alternate" <link> to its BGG page.
// now is this run's generation instant. rows are [rank, id, wins, link, name].
func updateFeedPerGame(path string, now time.Time, rows [][]string) error {
	feed, err := loadFeed(path)
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
		}
	}

	finalizeFeed(&feed, rankByID, now)
	return saveFeed(path, feed)
}

// finalizeFeed orders entries, caps by count, and sets the feed-level updated.
//
// rankByID maps an entry id to its rank in THIS run (nil/empty on the digest path).
// now is the fallback feed-updated for an empty feed.
func finalizeFeed(feed *atomFeed, rankByID map[string]int, now time.Time) {
	type keyedEntry struct {
		entry atomEntry
		when  time.Time
	}
	keyed := make([]keyedEntry, len(feed.Entry))
	capSafe := true
	var maxWhen time.Time
	for i, e := range feed.Entry {
		// The sort key is updated, NOT published. published is compared as a parsed
		// instant rather than by lexical string order because a later format or zone
		// change would silently misorder with nothing failing; parsing ties the sort
		// to the instant rather than the serialization.
		when, err := time.Parse(time.RFC3339, e.Updated)
		if err != nil {
			// Non-fatal, matching the feed path's policy of never aborting the sheet
			// output. A key we could not parse makes the cap unsafe this run, so record
			// a reason instead of guessing.
			fmt.Fprintf(os.Stderr, "feed: unparseable updated %q on entry %q: %v\n", e.Updated, e.ID, err)
			capSafe = false
		}
		if when.After(maxWhen) {
			maxWhen = when
		}
		keyed[i] = keyedEntry{entry: e, when: when}
	}

	// Primary key: freshness by updated (last change) DESCENDING, NOT published (first
	// publication). Under update-in-place a game hot in week 1 and still hot in week 6
	// keeps published=week-1, so a published sort would sink the longest-hot games to
	// the bottom — and the cap drops the bottom — in a feed whose entire purpose is
	// what is hot now.
	//
	// Tie key: current-run rank ASCENDING. A tie group shares one generation instant,
	// so by construction it is one run's cohort and the ranks are all from that same
	// run and genuinely comparable. A missing rank (an entry not in THIS run, or the
	// whole digest path) is +infinity, so unmapped entries sort last within a tie and
	// tie among themselves. +infinity, not "refuse to compare when either is unmapped":
	// gating that way makes the predicate non-transitive (A==B, C==B, yet C<A) — not a
	// strict weak ordering — and sort then yields an unspecified permutation with
	// nothing failing. Assigning the sentinel keeps a total order.
	//
	// This is also why nothing reads a stale rank even if updated ever loses resolution
	// (a date, not an instant) and cohorts merge into one tie group: an out-of-run
	// entry is not in rankByID, so it contributes +infinity, never a rank from a
	// different run. Keep this comparator keyed on an instant; a coarser updated would
	// merge cohorts on the primary key and is the way this guarantee is lost.
	rankOf := func(id string) int {
		if r, ok := rankByID[id]; ok {
			return r
		}
		return math.MaxInt
	}
	// SliceStable, not Slice: entries the comparator deems equal (same updated, same
	// rank sentinel) must keep insertion order. An unstable sort would permute them,
	// and the publish step's no-op guard is a byte-level `git diff --cached --quiet`,
	// so the permutation would manufacture a commit with no semantic change every run.
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

	// Feed-level updated is the most recent entry updated, so a run that changes no
	// entry produces byte-identical output and the publish step's no-op guard skips the
	// commit. An empty feed has no entry instant to borrow, so it falls back to
	// generation time.
	if maxWhen.IsZero() {
		feed.Updated = now.UTC().Format(time.RFC3339)
	} else {
		feed.Updated = maxWhen.UTC().Format(time.RFC3339)
	}
}

// sameLinks reports whether two link slices are element-wise equal. It is the change
// detector for the per-game Link field (condition 6): a differing link is a real
// change that must advance updated.
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
