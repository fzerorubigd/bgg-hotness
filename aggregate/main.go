package main

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/fzerorubigd/bggo"
	"resenje.org/schulze"
)

const (
	documentURL = "https://spreadsheets.google.com/feeds/download/spreadsheets/Export?key=%s&exportFormat=csv&gid=%d"
	batchSize   = 20
)

type Command struct {
	Command string                 `json:"command"`
	Args    map[string]interface{} `json:"args"`
}

func getCSV(ctx context.Context, doc string, page int, dateIn, dateOut time.Time) ([][]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf(documentURL, doc, page), nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()
	csReader := csv.NewReader(resp.Body)
	headers, err := csReader.Read()
	if err != nil {
		return nil, err
	}
	expected := make([]string, 51)

	expected[0] = "Date"
	for i := 1; i < len(expected); i++ {
		expected[i] = fmt.Sprint(i)
	}

	if len(headers) != len(expected) {
		return nil, fmt.Errorf("the header need to have exactly %d items but has %d", len(expected), len(headers))
	}

	for i := range expected {
		if expected[i] != headers[i] {
			return nil, fmt.Errorf("headers do not match %s => %s", expected[i], headers[i])
		}
	}

	var res [][]string
	for {
		ln, err := csReader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		date, err := time.Parse(time.DateOnly, ln[0])
		if err != nil {
			// Err?
			continue
		}
		if date.After(dateIn) && date.Before(dateOut) {
			res = append(res, ln)
		}
	}

	return res, nil
}

func options(in [][]string) []string {
	m := make(map[string]struct{})
	for i := range in {
		for _, v := range in[i][1:] {
			m[v] = struct{}{}
		}
	}

	ret := make([]string, 0, len(m))
	for i := range m {
		ret = append(ret, i)
	}

	return ret
}

func toMap(in []string) schulze.Ballot[string] {
	res := schulze.Ballot[string]{}
	for i, v := range in[1:] {
		res[v] = i + 1
	}

	return res
}

// aggregationPeriod computes the date window [dayIn, dayOut] and the worksheet/feed
// title for a run. now is injected rather than read internally so both the rolling
// and the -year/-month paths are testable — and so the invariant the feed's
// published field rests on is checkable: published is the END OF THE PERIOD THIS
// ENTRY DESCRIBES. On the rolling path a window ends now, so dayOut is wall-clock
// and that is correct as written; on -year/-month it is a fixed period-end instant.
// Validation of year/month stays in the caller so this function is pure.
//
// The title is deliberately built from the pre-clamp days, matching prior
// behaviour; the clamp to [7, 500] applies only to the window.
func aggregationPeriod(now time.Time, days, year, month int) (dayIn, dayOut time.Time, title string) {
	title = fmt.Sprintf("%s_%d-days", now.Format(time.DateOnly), days)
	if days < 7 {
		days = 7
	}
	if days > 500 {
		days = 500
	}
	window := time.Hour * 24 * time.Duration(days)
	dayIn, dayOut = now.Add(-window), now
	if year != 0 {
		if month != 0 {
			dayIn = time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.Local)
			dayOut = time.Date(year, time.Month(month)+1, 1, 0, 0, 0, 0, time.Local).Add(-time.Second)
			title = fmt.Sprintf("Monthly - %d-%d", year, month)
		} else {
			dayIn = time.Date(year, 1, 1, 0, 0, 0, 0, time.Local)
			dayOut = time.Date(year+1, 1, 1, 0, 0, 0, 0, time.Local).Add(-time.Second)
			title = fmt.Sprintf("Yearly - %d", year)
		}
	}
	return dayIn, dayOut, title
}

func main() {
	ctx, cnl := signal.NotifyContext(context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGQUIT,
		syscall.SIGABRT)
	defer cnl()

	var (
		documentID string
		pageID     int
		days       int
		year       int
		month      int
		count      int
	)
	flag.StringVar(&documentID, "document-id", os.Getenv("DOCUMENT_ID"), "The document id to get the data from")
	flag.IntVar(&pageID, "page-id", 0, "The page id in document")
	flag.IntVar(&days, "days", 14, "Number of days to get the report, will be ignored if year is set")
	flag.IntVar(&year, "year", 0, "Year to get the report, if set, the days will be ignored")
	flag.IntVar(&month, "month", 0, "Month to get the report, if set, year should be sert too")
	flag.IntVar(&count, "count", 50, "Number of items to get the report")
	flag.Parse()

	if year != 0 {
		if year < 2023 || year > time.Now().Year() {
			log.Fatal("there is no data before mid 2023")
		}
		if month != 0 && (month < 1 || month > 12) {
			log.Fatal("month should be between 1 and 12")
		}
	}
	dayIn, dayOut, today := aggregationPeriod(time.Now(), days, year, month)

	ballots, err := getCSV(ctx, documentID, pageID, dayIn, dayOut)
	if err != nil {
		log.Fatal(err)
	}

	choices := options(ballots)
	preferences := schulze.NewPreferences(len(choices))

	for i := range ballots {
		if _, err := schulze.Vote(preferences, choices, toMap(ballots[i])); err != nil {
			log.Fatal(err)
		}
	}

	result, _, _ := schulze.Compute(preferences, choices)
	ids := make([]int64, 0, count)
	for i := range result {
		if i >= count {
			break
		}
		id, err := strconv.ParseInt(result[i].Choice, 10, 0)
		if err != nil {
			log.Fatal(err)
		}
		ids = append(ids, id)
	}
	token := os.Getenv("BGG_TOKEN")
	if token == "" {
		panic("BGG_TOKEN is not set")
	}
	c := bggo.NewClient(token)
	// Size to the number of ranked ids actually produced, not the requested count:
	// the Schulze result can yield fewer distinct choices than count, and a
	// count-sized slice leaves the tail nil, which is marshalled to the sheet (and
	// would be rendered into the feed) as empty rows. Pre-existing; fixed here in
	// passing because the feed is what would make those empty rows user-visible.
	data := make([][]string, len(ids))
	for idx := 0; idx < len(ids); idx += batchSize {
		var nextBatch []int64
		if len(ids)-idx < batchSize {
			nextBatch = ids[idx:]
		} else {
			nextBatch = ids[idx : idx+batchSize]
		}
		things, err := c.GetThings(ctx, bggo.GetThingsRequest{IDs: nextBatch})
		if err != nil {
			panic(err)
		}

		// BGG returns things in its own order and silently drops invalid/retired
		// IDs, so index the results by ID and look up each requested id rather than
		// assuming the response aligns positionally with the request.
		byID := make(map[int64]bggo.ThingResult, len(things))
		for _, t := range things {
			byID[t.ID] = t
		}

		for i, id := range nextBatch {
			// On a miss (id dropped upstream), emit the row with the known id and a
			// blank name rather than panicking. Rank (i+idx+1) and Wins
			// (result[i+idx]) come from the Schulze order and are correct (PR #170).
			name := ""
			if t, ok := byID[id]; ok {
				name = t.Name
			}
			data[i+idx] = append(data[i+idx],
				fmt.Sprint(i+idx+1),
				fmt.Sprint(id),
				fmt.Sprint(result[i+idx].Wins),
				fmt.Sprintf("https://boardgamegeek.com/boardgame/%d/", id),
				name)
		}
	}

	base := []string{
		"Rank",
		"BGGID",
		"Wins",
		"Link",
		"Name",
	}
	data = append([][]string{base}, data...)

	rs := fmt.Sprintf("%s!A1:E%d", today, len(data))
	commands := []Command{
		{
			Command: "addWorksheet",
			Args: map[string]interface{}{
				"worksheetTitle": today,
			},
		},
		{
			Command: "updateData",
			Args: map[string]interface{}{
				"minCol":         1,
				"data":           data,
				"range":          rs,
				"worksheetTitle": today,
			},
		},
	}
	x, err := json.Marshal(commands)
	if err != nil {
		panic(err)
	}
	sum := sha256.New()
	fmt.Fprint(sum, time.Now())
	eof := fmt.Sprintf("%x", sum.Sum(nil))
	fmt.Printf("data_array<<%s\n", eof)
	fmt.Println(string(x))
	fmt.Println(eof)

	// Publish an Atom feed entry for this run, but only after the heredoc above is
	// on stdout — stdout here is the Actions output protocol the sheet update
	// consumes, so the additive feed must not be able to regress it. Any feed
	// failure logs to stderr and returns rather than aborting, for the same reason.
	// data[1:] is the ranked rows; data[0] is the header prepended above.
	if feedFile := os.Getenv("FEED_FILE"); feedFile != "" {
		if err := updateFeed(feedFile, today, dayOut, time.Now(), data[1:]); err != nil {
			fmt.Fprintf(os.Stderr, "feed: %v (sheet output unaffected)\n", err)
		}
	}
}
