package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/fzerorubigd/bggo"
	"go.uber.org/ratelimit"
)

type Command struct {
	Command string                 `json:"command"`
	Args    map[string]interface{} `json:"args"`
}

func main() {
	ctx, cnl := signal.NotifyContext(context.Background(),
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGQUIT,
		syscall.SIGABRT)
	defer cnl()
	rl := ratelimit.New(1, ratelimit.Per(time.Second)) // 1 request per second.
	token := os.Getenv("BGG_TOKEN")
	if token == "" {
		panic("BGG_TOKEN is not set")
	}
	c := bggo.NewClient(token, bggo.WithLimiter(rl))
	hot, err := c.GetHotness(ctx, bggo.GetHotnessRequest{Count: 50})
	if err != nil {
		panic(err)
	}

	// bggo's HotnessItem carries Name inline, so the name column is filled here
	// directly — no second batched name lookup, and no positional-index hazard.
	data := make([][]string, len(hot))
	aggregate := make([]string, len(hot))
	for i := range hot {
		aggregate[i] = fmt.Sprint(hot[i].ID)
		data[i] = append(data[i],
			fmt.Sprint(i+1),
			fmt.Sprint(hot[i].ID),
			fmt.Sprint(hot[i].Delta),
			fmt.Sprintf("https://boardgamegeek.com/boardgame/%d/", hot[i].ID),
			hot[i].Name,
		)
	}

	base := []string{
		"Rank",
		"BGGID",
		"Change",
		"Link",
		"Name",
	}
	data = append([][]string{base}, data...)

	today := time.Now().Format(time.DateOnly)
	rs := fmt.Sprintf("%s!A1:E%d", today, len(data))
	aggregate = append([]string{today}, aggregate...)
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
		{
			Command: "appendData",
			Args: map[string]interface{}{
				"minCol":         1,
				"data":           [][]string{aggregate},
				"worksheetTitle": "Aggregate",
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
