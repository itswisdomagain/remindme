package main

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"strconv"

	"go.etcd.io/bbolt"
)

var (
	db *bbolt.DB

	categoriesBkt = []byte("categories")
	lastRunBktKey = []byte("last_run")

	itemContentKey = []byte("content")
	itemTypeKey    = []byte("type")
)

type Category struct {
	Name  string  `json:"name"`
	Items []*Item `json:"items"`
}

type Item struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Content []byte `json:"Content"`
}

func downloadFromAPI() ([]string, error) {
	resp, err := http.Get("http://64.225.13.138:17778/api/items")
	if err != nil {
		return nil, err
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	catItems := make([]*Category, 0)
	err = json.Unmarshal(body, &catItems)
	if err != nil {
		return nil, err
	}

	categories := make([]string, 0, len(catItems))

	return categories, db.Update(func(tx *bbolt.Tx) error {
		for _, category := range catItems {
			categories = append(categories, category.Name)

			catsBucket, err := tx.CreateBucketIfNotExists(categoriesBkt)
			if err != nil {
				return fmt.Errorf("failed to open db record for all categories")
			}

			catBucket, err := catsBucket.CreateBucketIfNotExists([]byte(category.Name))
			if err != nil {
				return fmt.Errorf("failed to open db record for %s", category.Name)
			}

			for _, item := range category.Items {
				itemBucket, err := catBucket.CreateBucketIfNotExists([]byte(item.Name))
				if err != nil {
					return fmt.Errorf("failed to open db record for %s", item.Name)
				}
				if err = itemBucket.Put(itemTypeKey, []byte(item.Type)); err != nil {
					return err
				}
				if err = itemBucket.Put(itemContentKey, item.Content); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func categoriesFromDB() (categories []string, err error) {
	err = db.View(func(tx *bbolt.Tx) error {
		catsBucket := tx.Bucket(categoriesBkt)
		if catsBucket == nil {
			return nil
		}
		cats := catsBucket.Cursor()
		for categoryB, _ := cats.First(); categoryB != nil; categoryB, _ = cats.Next() {
			categories = append(categories, string(categoryB))
		}
		return nil
	})
	return
}

func categoryItems(category string) (items []*Item, err error) {
	err = db.View(func(tx *bbolt.Tx) error {
		catsBucket := tx.Bucket(categoriesBkt)
		if catsBucket == nil {
			return fmt.Errorf("no data downloaded yet!")
		}
		categoryBkt := catsBucket.Bucket([]byte(category))
		if categoryBkt == nil {
			return fmt.Errorf("unknown reminder category: %s", category)
		}
		categoryItems := categoryBkt.Cursor()
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
		return nil
	})
	return
}

func saveLastRun(category string, index int) error {
	return db.Update(func(tx *bbolt.Tx) error {
		lastRunBkt, err := tx.CreateBucketIfNotExists(lastRunBktKey)
		if err != nil {
			return err
		}
		return lastRunBkt.Put([]byte(category), []byte(strconv.Itoa(index)))
	})
}

func deleteLastRun(category string) error {
	return db.Update(func(tx *bbolt.Tx) error {
		lastRunBkt, err := tx.CreateBucketIfNotExists(lastRunBktKey)
		if err != nil {
			return err
		}
		return lastRunBkt.Delete([]byte(category))
	})
}

func lastRuns() (map[string]int, error) {
	lastRuns := make(map[string]int)
	return lastRuns, db.View(func(tx *bbolt.Tx) error {
		lastRunBkt := tx.Bucket(lastRunBktKey)
		if lastRunBkt == nil {
			return nil
		}
		records := lastRunBkt.Cursor()
		for catB, indexB := records.First(); catB != nil; catB, _ = records.Next() {
			index, err := strconv.Atoi(string(indexB))
			if err != nil {
				fmt.Println(err.Error())
				continue
			}
			lastRuns[string(catB)] = index
		}
		return nil
	})
}
