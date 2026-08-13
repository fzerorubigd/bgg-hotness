package main

import (
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"os"
	"sort"
	"strings"
	"time"
)

// feedCap bounds the feed to its most recent entries by count. It is a
// nice-to-have — the entries are the product — so it is given up entirely for any
// run whose sort key could not be fully parsed (see updateFeed).
const feedCap = 200

// Atom feed output. The aggregate job renders one entry per run into an Atom
// feed committed to a dedicated branch; a reader (my-rss) subscribes to the raw
// URL. Entry identity is keyed off the run's title (the same value used as the
// sheet worksheet title), so a job whose title carries no date — the yearly
// aggregate — replaces its entry on re-dispatch instead of appending a duplicate,
// while the date-bearing days-based jobs append a new entry each run.

const (
	feedTitle  = "BGG Hotness Aggregates"
	feedID     = "tag:github.com,2024:bgg-hotness:feed"
	authorName = "bgg-hotness"
	// tagPrefix + slug(title) is the per-entry id. A tag: URI is used rather than
	// the raw feed URL because it is stable regardless of where the feed is hosted,
	// so a reader's dedupe survives the feed being moved or mirrored.
	tagPrefix = "tag:github.com,2024:bgg-hotness:"
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
	Title     string      `xml:"title"`
	ID        string      `xml:"id"`
	Published string      `xml:"published"`
	Updated   string      `xml:"updated"`
	Content   atomContent `xml:"content"`
}

type atomContent struct {
	Type string `xml:"type,attr"`
	Text string `xml:",chardata"`
}

// updateFeed reads the existing feed at path (treating an absent file as an empty
// feed), inserts or replaces the entry for this run, and writes the result back.
// published is the end of the aggregated period; updated is generation time — so a
// backfilled old period sits in its correct chronological place yet still surfaces
// as new in a reader. rows are the ranked game rows [rank, id, wins, link, name].
func updateFeed(path, title string, published, updated time.Time, rows [][]string) error {
	feed := atomFeed{}
	if b, err := os.ReadFile(path); err == nil {
		if err := xml.Unmarshal(b, &feed); err != nil {
			return fmt.Errorf("parse existing feed %q: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read existing feed %q: %w", path, err)
	}

	// Reassert the canonical feed-level fields so an older or hand-edited feed
	// converges rather than preserving stale values, and clear XMLName so the
	// struct tag (not the value parsed from disk) supplies the Atom namespace on
	// marshal.
	feed.XMLName = xml.Name{}
	feed.Title = feedTitle
	feed.ID = feedID
	feed.Author = atomAuthor{Name: authorName}
	feed.Updated = updated.UTC().Format(time.RFC3339)

	entry := atomEntry{
		Title:     title,
		ID:        tagPrefix + slug(title),
		Published: published.UTC().Format(time.RFC3339),
		Updated:   updated.UTC().Format(time.RFC3339),
		Content:   atomContent{Type: "html", Text: renderContent(rows)},
	}

	replaced := false
	for i := range feed.Entry {
		if feed.Entry[i].ID == entry.ID {
			feed.Entry[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		feed.Entry = append([]atomEntry{entry}, feed.Entry...)
	}

	// Order newest-first by the period each entry describes, then cap by count.
	// The ordering is load-bearing: the insert-or-replace above ran against the
	// FULL existing set, and the cap is applied only AFTER — parse the whole set,
	// insert/replace, sort, truncate, write. Truncating before the replace scan
	// would hide an entry past the cap from that scan, the only way a duplicate id
	// could be produced.
	//
	// published is compared as a parsed instant, not by lexical string order:
	// lexical order is chronological only because every value is written via
	// .UTC().Format(time.RFC3339) as a fixed-width Z form, so a later format or
	// zone change would silently misorder with nothing failing. Parsing ties the
	// sort to the instant rather than the serialization.
	type keyedEntry struct {
		entry atomEntry
		when  time.Time
	}
	keyed := make([]keyedEntry, len(feed.Entry))
	capSafe := true
	for i, e := range feed.Entry {
		when, err := time.Parse(time.RFC3339, e.Published)
		if err != nil {
			// Non-fatal, matching the feed path's policy of never aborting the
			// sheet output. A key we could not parse makes the cap unsafe this
			// run — enforcing it would delete on an ordering derived from an
			// incomplete key set — so record a reason instead of guessing.
			fmt.Fprintf(os.Stderr, "feed: unparseable published %q on entry %q: %v\n", e.Published, e.ID, err)
			capSafe = false
		}
		keyed[i] = keyedEntry{entry: e, when: when}
	}
	// SliceStable, not Slice: tied published values must keep insertion order.
	// An unstable sort would permute ties arbitrarily, and the publish step's
	// no-op guard is a byte-level `git diff --cached --quiet`, so the permutation
	// would manufacture a commit with no semantic change on every run.
	sort.SliceStable(keyed, func(i, j int) bool { return keyed[i].when.After(keyed[j].when) })

	// Cap by count, but skip truncation entirely for any run where a key failed to
	// parse — the skip is per RUN, not per entry: dropping "only" the sorted tail
	// still deletes on an incomplete ordering. When skipped, the feed drifts over
	// the cap until the stderr line is acted on; nothing is lost. The cap is a
	// nice-to-have; the entries are the product.
	keep := len(keyed)
	if capSafe && keep > feedCap {
		keep = feedCap
	}
	feed.Entry = make([]atomEntry, keep)
	for i := 0; i < keep; i++ {
		feed.Entry[i] = keyed[i].entry
	}

	out, err := xml.MarshalIndent(&feed, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal feed: %w", err)
	}
	out = append([]byte(xml.Header), append(out, '\n')...)

	// Write to a temp file and rename so a crash mid-write cannot leave a truncated
	// feed that the next run would fail to parse.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// renderContent builds the entry body as an ordered list of ranked games, each a
// BGG link. It is emitted as Atom content type="html"; encoding/xml escapes the
// whole string once as chardata, so the inner HTML is additionally html-escaped
// here to stay well-formed after a reader un-escapes it.
func renderContent(rows [][]string) string {
	var b strings.Builder
	b.WriteString("<ol>")
	for _, r := range rows {
		if len(r) < 5 {
			continue
		}
		name := r[4]
		if name == "" {
			// BGG dropped this id upstream (retired/invalid); show the id rather
			// than an empty link so the entry stays legible.
			name = "BGG #" + r[1]
		}
		fmt.Fprintf(&b, "<li><a href=\"%s\">%s</a> — %s wins</li>",
			html.EscapeString(r[3]), html.EscapeString(name), html.EscapeString(r[2]))
	}
	b.WriteString("</ol>")
	return b.String()
}

// slug reduces a run title to a stable, URI-safe fragment for the entry id:
// lowercase, with each run of non-alphanumeric characters collapsed to a single
// dash. The same title always yields the same slug, which is what gives the
// yearly job's re-dispatch its replace-not-append behaviour.
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
