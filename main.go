package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/decred/dcrd/dcrutil/v3"
	"go.etcd.io/bbolt"
)

func main() {
	appDataDir := dcrutil.AppDataDir("remindme", false)
	err := os.MkdirAll(appDataDir, 0700)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create app data directory: %v\n", err)
		os.Exit(1)
	}

	dbPath := filepath.Join(appDataDir, "bdb.db")
	db, err := bbolt.Open(dbPath, 0600, &bbolt.Options{Timeout: 1 * time.Second})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to open database: %v\n", err)
		os.Exit(1)
	}

	defer func() {
		if err := db.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "failed to close db: %v\n", err)
		}
	}()

	ctx, cancel := context.WithCancel(context.Background())

	// Start a goroutine to catch interrupt signal (e.g. ctrl+c)
	// before starting the api server. On interrupt, kill the
	// ctx associated with the api server to signal the api
	// server to stop.
	killChan := make(chan os.Signal)
	signal.Notify(killChan, os.Interrupt)
	go func() {
		for range killChan {
			fmt.Println("shutting down...")
			cancel()
			break
		}
	}()

	api := &apiServer{
		db: db,
	}

	// go func() {
	// 	time.Sleep(5 * time.Second)
	// 	resp, err := http.Get("http://0.0.0.0:54321/api/items")
	// 	if err != nil {
	// 		fmt.Println(err.Error())
	// 		return
	// 	}
	// 	body, err := ioutil.ReadAll(resp.Body)
	// 	if err != nil {
	// 		fmt.Println(err.Error())
	// 		return
	// 	}
	// 	cats := make([]*Category, 0)
	// 	err = json.Unmarshal(body, &cats)
	// 	if err != nil {
	// 		fmt.Fprintf(os.Stderr, "failed to unmarshal JSON request: %v\n", err)
	// 		return
	// 	}
	// 	item := cats[0].Items[1]
	// 	println("o")
	// 	println(len(item.Attachment))
	// }()

	if err := api.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "api start error: %v\n", err)
	}
}
