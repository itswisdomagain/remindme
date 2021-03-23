package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/go-chi/chi"
	"go.etcd.io/bbolt"
)

type apiServer struct {
	db *bbolt.DB
}

func (api *apiServer) Start(ctx context.Context) error {
	// Create an HTTP router.
	mux := chi.NewRouter()

	// Mount api endpoints.
	mux.Route("/api", func(r chi.Router) {
		r.Get("/items", api.allItems)
		r.Post("/items", api.storeItem)
	})

	// Get ready to serve the API.
	listenAddr := "0.0.0.0:17778"
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("Can't listen on %s. web server quitting: %w", listenAddr, err)
	}
	httpServer := &http.Server{
		Handler: mux,
	}

	// Listen for context cancellation in bg and kill the server.
	go func() {
		<-ctx.Done()
		if err := httpServer.Shutdown(context.Background()); err != nil {
			fmt.Fprintf(os.Stderr, "api server shutdown error: %v\n", err)
			os.Exit(1)
		}
	}()

	// Start server in bg.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		err = httpServer.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
	}()

	fmt.Printf("API live on http://%s\n", listenAddr)
	wg.Wait()

	return err
}

var (
	itemContentKey = []byte("content")
	itemTypeKey    = []byte("type")
)

type Item struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content []byte `json:"Content"`
}

const maxFileBytes = 10_000_000 // 10mb

func (api *apiServer) storeItem(w http.ResponseWriter, r *http.Request) {
	r.ParseMultipartForm(maxFileBytes)

	category := r.FormValue("category")
	itemName := r.FormValue("item.name")
	itemType := strings.ToLower(r.FormValue("item.type"))
	itemContent := r.FormValue("item.content")

	var content []byte
	var hasAttachment bool
	f, h, err := r.FormFile("item.attachment")
	if err != nil && !errors.Is(err, http.ErrMissingFile) {
		fmt.Fprintf(os.Stderr, "Error reading file attachment: %v\n", err)
		http.Error(w, "error reading file attachment", http.StatusInternalServerError)
		return
	}
	if f != nil {
		hasAttachment = true
		buf := bytes.NewBuffer(nil)
		_, err = io.Copy(buf, f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "file bytes copy error: %v\n", err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		content = buf.Bytes()
	} else {
		content = []byte(itemContent)
	}

	switch hasAttachment {
	case true:
		fileType := strings.ToLower(h.Header.Get("Content-Type"))
		if !strings.HasPrefix(fileType, itemType) {
			http.Error(w, "invalid attachment for "+itemType, http.StatusBadRequest)
			return
		}

	case false:
		if itemType == "video" || itemType == "image" {
			http.Error(w, "video or image requires attachment", http.StatusBadRequest)
			return
		}
	}

	err = api.db.Update(func(tx *bbolt.Tx) error {
		catBucket, err := tx.CreateBucketIfNotExists([]byte(category))
		if err != nil {
			return fmt.Errorf("failed to open db record for %s", category)
		}
		itemBucket, err := catBucket.CreateBucketIfNotExists([]byte(itemName))
		if err != nil {
			return fmt.Errorf("failed to open db record for %s", itemName)
		}
		if err = itemBucket.Put(itemTypeKey, []byte(itemType)); err != nil {
			return err
		}
		if err = itemBucket.Put(itemContentKey, content); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error saving item with attachment (%v): %v\n", hasAttachment, err)
		http.Error(w, "error saving item", http.StatusInternalServerError)
		return
	}

	api.allItems(w, r)
}

type Category struct {
	Name  string  `json:"name"`
	Items []*Item `json:"items"`
}

func (api *apiServer) allItems(w http.ResponseWriter, r *http.Request) {
	categoriesWithItems := make([]*Category, 0)

	err := api.db.View(func(tx *bbolt.Tx) error {
		categories := tx.Cursor()
		for categoryB, _ := categories.First(); categoryB != nil; categoryB, _ = categories.Next() {
			category := string(categoryB)
			categoryBkt := tx.Bucket(categoryB)
			if categoryBkt == nil {
				fmt.Fprintf(os.Stderr, "category %s not a db bucket\n", category)
				continue
			}

			categoryItems := categoryBkt.Cursor()
			items := make([]*Item, 0)
			for itemB, _ := categoryItems.First(); itemB != nil; itemB, _ = categoryItems.Next() {
				itemName := string(itemB)
				itemBkt := categoryBkt.Bucket(itemB)
				if itemBkt == nil {
					fmt.Fprintf(os.Stderr, "item %s not a nested db bucket in %s\n", itemName, category)
					continue
				}
				itemType := itemBkt.Get(itemTypeKey)
				items = append(items, &Item{
					Name:    itemName,
					Type:    string(itemType),
					Content: itemBkt.Get(itemContentKey),
				})
			}

			categoriesWithItems = append(categoriesWithItems, &Category{
				Name:  category,
				Items: items,
			})
		}
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error fetching items from db: %v\n", err)
		http.Error(w, "error fetching items", http.StatusInternalServerError)
		return
	}

	writeJSON(w, categoriesWithItems)
}

// writeJSON marshals the provided interface and writes the bytes to the
// ResponseWriter. The response code is assumed to be StatusOK.
func writeJSON(w http.ResponseWriter, thing interface{}) {
	writeJSONWithStatus(w, thing, http.StatusOK)
}

// writeJSON writes marshals the provided interface and writes the bytes to the
// ResponseWriter with the specified response code.
func writeJSONWithStatus(w http.ResponseWriter, thing interface{}, code int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	b, err := json.Marshal(thing)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(os.Stderr, "JSON encode error: %v\n", err)
		return
	}
	w.WriteHeader(code)
	_, err = w.Write(append(b, byte('\n')))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Write error: %v\n", err)
	}
}
