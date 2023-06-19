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
	"syscall"
	"time"
)

const (
	documentURL = "http://spreadsheets.google.com/feeds/download/spreadsheets/Export?key=%s&exportFormat=csv&gid=%d"
)

type Command struct {
	Command string                 `json:"command"`
	Args    map[string]interface{} `json:"args"`
}

func getCSV(ctx context.Context, doc string, page int, date time.Time) (map[int]time.Time, error) {
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
	expected := make([]string, 51, 52)

	expected[0] = "Date"
	for i := 1; i < len(expected); i++ {
		expected[i] = fmt.Sprint(i)
	}
	expected = append(expected, "deleted")

	if len(headers) != len(expected) {
		return nil, fmt.Errorf("the header need to have exactly %d items", len(expected))
	}

	for i := range expected {
		if expected[i] != headers[i] {
			return nil, fmt.Errorf("headers do not match %s => %s", expected[i], headers[i])
		}
	}

	res := make(map[int]time.Time)
	row := 1
	for {
		row++
		ln, err := csReader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		rowDate, err := time.Parse(time.DateOnly, ln[0])
		if err != nil {
			// Err?
			continue
		}
		if rowDate.Before(date) && ln[len(ln)-1] == "" {
			res[row] = rowDate
		}
	}

	return res, nil
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
	)
	flag.StringVar(&documentID, "document-id", os.Getenv("DOCUMENT_ID"), "The document id to get the data from")
	flag.IntVar(&pageID, "page-id", 0, "The page id in document")
	flag.IntVar(&days, "days", 14, "Number of days to get the report")
	flag.Parse()

	if days < 7 {
		days = 7
	}
	if days > 90 {
		days = 90
	}
	weeks := time.Hour * 24 * time.Duration(days)
	toDelete, err := getCSV(ctx, documentID, pageID, time.Now().Add(-weeks))
	if err != nil {
		log.Fatal(err)
	}

	var commands []Command
	for i, d := range toDelete {
		title := d.Format(time.DateOnly)

		commands = append(commands, Command{
			Command: "updateData",
			Args: map[string]interface{}{
				"minCol":         1,
				"data":           [][]string{{"X"}},
				"range":          fmt.Sprintf("Aggregate!AZ%d", i),
				"worksheetTitle": "Aggregate",
			},
		}, Command{
			Command: "removeWorksheet",
			Args: map[string]interface{}{
				"worksheetTitle": title,
			},
		})
	}

	// To make sure the commands are never empty
	commands = append(commands, Command{
		Command: "getData",
		Args: map[string]interface{}{
			"minCol":         1,
			"range":          "Aggregate!AZ1",
			"worksheetTitle": "Aggregate",
		},
	})
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
