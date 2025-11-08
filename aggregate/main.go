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

	"github.com/fzerorubigd/gobgg"
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

func main() {
	ctx, cnl := signal.NotifyContext(context.Background(),
		syscall.SIGKILL,
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

	today := fmt.Sprintf("%s_%d-days", time.Now().Format(time.DateOnly), days)
	if days < 7 {
		days = 7
	}
	if days > 500 {
		days = 500
	}
	weeks := time.Hour * 24 * time.Duration(days)
	dayIn, dayOut := time.Now().Add(-weeks), time.Now()
	if year != 0 {
		if year < 2023 || year > time.Now().Year() {
			log.Fatal("there is no data before mid 2023")
		}

		if month != 0 {
			if month < 1 || month > 12 {
				log.Fatal("month should be between 1 and 12")
			}
			dayIn = time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.Local)
			dayOut = time.Date(year, time.Month(month)+1, 1, 0, 0, 0, 0, time.Local).Add(-time.Second)
			today = fmt.Sprintf("Monthly - %d-%d", year, month)
		} else {
			dayIn = time.Date(year, 1, 1, 0, 0, 0, 0, time.Local)
			dayOut = time.Date(year+1, 1, 1, 0, 0, 0, 0, time.Local).Add(-time.Second)
			today = fmt.Sprintf("Yearly - %d", year)
		}

	}

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
	bgg := gobgg.NewBGGClient(gobgg.SetAuthToken(token))
	data := make([][]string, count)
	for idx := 0; idx < len(ids); idx += batchSize {
		var nextBatch []int64
		if len(ids)-idx < batchSize {
			nextBatch = ids[idx:]
		} else {
			nextBatch = ids[idx : idx+batchSize]
		}
		things, err := bgg.GetThings(ctx, gobgg.GetThingIDs(nextBatch...))
		if err != nil {
			panic(err)
		}

		for i := range nextBatch {
			data[i+idx] = append(data[i+idx],
				fmt.Sprint(i+idx+1),
				fmt.Sprint(things[i].ID),
				fmt.Sprint(result[i].Wins),
				fmt.Sprintf("https://boardgamegeek.com/boardgame/%d/", things[i].ID),
				things[i].Name)
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
}
