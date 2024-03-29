package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os/signal"
	"syscall"
	"time"

	"github.com/fzerorubigd/gobgg"
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
	bgg := gobgg.NewBGGClient()
	hot, err := bgg.Hotness(ctx, 50)
	if err != nil {
		panic(err)
	}

	ids := make([]int64, len(hot))
	data := make([][]string, len(hot))
	aggregate := make([]string, len(hot))
	for i := range hot {
		ids[i] = hot[i].ID
		aggregate[i] = fmt.Sprint(hot[i].ID)
		data[i] = append(data[i],
			fmt.Sprint(i+1),
			fmt.Sprint(hot[i].ID),
			fmt.Sprint(hot[i].Delta),
			fmt.Sprintf("https://boardgamegeek.com/boardgame/%d/", hot[i].ID),
		)
	}

	things, err := bgg.GetThings(ctx, gobgg.GetThingIDs(ids...))
	if err != nil {
		panic(err)
	}

	for i := range things {
		data[i] = append(data[i], things[i].Name)
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
