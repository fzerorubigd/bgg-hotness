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
	documentURL = "http://spreadsheets.google.com/feeds/download/spreadsheets/Export?key=%s&exportFormat=csv&gid=%d"
)

type Command struct {
	Command string                 `json:"command"`
	Args    map[string]interface{} `json:"args"`
}

func getCSV(ctx context.Context, doc string, page int, dateIn, dateOut time.Time) ([][]string, error) {
	resp, err := http.Get(fmt.Sprintf(documentURL, doc, page))
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
		return nil, fmt.Errorf("the header need to have exactly %d items", len(expected))
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
	os.Setenv("DOCUMENT_ID", "1b5gxf72Txb6vBEG-q-W7vkPUyn9fLmQ92DxKq7zAyqg")
	var (
		DocumentID string
		PageID     int
	)
	flag.StringVar(&DocumentID, "document-id", os.Getenv("DOCUMENT_ID"), "The document id to get the data from")
	flag.IntVar(&PageID, "page-id", 0, "The page id in document")

	flag.Parse()
	weeks := time.Hour * 24 * 14
	ballots, err := getCSV(ctx, DocumentID, PageID, time.Now().Add(-weeks), time.Now())
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

	result, _ := schulze.Compute(preferences, choices)
	ids := make([]int64, 0, 50)
	for i := range result {
		if i >= cap(ids) {
			break
		}
		id, err := strconv.ParseInt(result[i].Choice, 10, 0)
		if err != nil {
			log.Fatal(err)
		}
		ids = append(ids, id)
	}
	bgg := gobgg.NewBGGClient()
	things, err := bgg.GetThings(ctx, gobgg.GetThingIDs(ids...))
	if err != nil {
		log.Fatal(err)
	}
	data := make([][]string, len(things))
	for i := range things {
		data[i] = append(data[i],
			fmt.Sprint(i+1),
			fmt.Sprint(things[i].ID),
			fmt.Sprint(result[i].Wins),
			fmt.Sprintf("https://boardgamegeek.com/boardgame/%d/", things[i].ID),
			things[i].Name,
		)
	}

	base := []string{
		"Rank",
		"BGGID",
		"Wins",
		"Link",
		"Name",
	}
	data = append([][]string{base}, data...)

	today := time.Now().Format(time.DateOnly) + "_2week"
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
