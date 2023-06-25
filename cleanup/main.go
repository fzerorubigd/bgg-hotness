package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"regexp"
	"syscall"
	"time"

	"golang.org/x/oauth2/jwt"
	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

type Command struct {
	Command string                 `json:"command"`
	Args    map[string]interface{} `json:"args"`
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
		spreadsheetId string
		pageID        int
		days          int
	)
	flag.StringVar(&spreadsheetId, "document-id", os.Getenv("DOCUMENT_ID"), "The document id to get the data from")
	flag.IntVar(&pageID, "page-id", 0, "The page id in document")
	flag.IntVar(&days, "days", 14, "Number of days to get the report")
	flag.Parse()

	// Create a JWT configurations object for the Google service account
	conf := &jwt.Config{
		Email:      os.Getenv("GSHEET_CLIENT_EMAIL"),
		PrivateKey: []byte(os.Getenv("GSHEET_PRIVATE_KEY")),
		TokenURL:   "https://oauth2.googleapis.com/token",
		Scopes: []string{
			"https://www.googleapis.com/auth/spreadsheets",
		},
	}
	client := conf.Client(ctx)

	// Create a service object for Google sheets
	srv, err := sheets.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		log.Fatalf("Unable to retrieve Sheets client: %v", err)
	}

	sp, err := srv.Spreadsheets.Get(spreadsheetId).Context(ctx).Do()
	if err != nil {
		panic(err)
	}

	if days < 7 {
		days = 7
	}
	if days > 90 {
		days = 90
	}
	dateIn := time.Now().Add(-time.Hour * 24 * time.Duration(days))
	pattern := regexp.MustCompile("^[0-9]{4}-[0-9]{2}-[0-9]{2}")
	var commands []Command
	for _, sh := range sp.Sheets {
		dt := pattern.Find([]byte(sh.Properties.Title))
		if len(dt) == 0 {
			continue
		}
		date, err := time.Parse(time.DateOnly, string(dt))
		if err != nil {
			continue
		}

		if date.Before(dateIn) {
			commands = append(commands, Command{
				Command: "removeWorksheet",
				Args: map[string]interface{}{
					"worksheetTitle": sh.Properties.Title,
				},
			})
		}
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
