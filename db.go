package main

import (
	"fmt"

	"go.etcd.io/bbolt"
)

// Store stores a value at the specified key in the general-use bucket.
func (api *apiServer) Store(b string, k string, v []byte) error {
	if len(k) == 0 || len(b) == 0 {
		return fmt.Errorf("cannot store with empty bucket or key")
	}
	bucketByte := []byte(b)
	keyB := []byte(k)
	return api.db.Update(func(tx *bbolt.Tx) error {
		bucket, err := tx.CreateBucketIfNotExists(bucketByte)
		if err != nil {
			return fmt.Errorf("failed to create key bucket")
		}
		return bucket.Put(keyB, v)
	})
}

// Get retrieves value previously stored with Store.
func (api *apiServer) Get(b string, k string) ([]byte, error) {
	if len(k) == 0 || len(b) == 0 {
		return nil, fmt.Errorf("cannot get with empty bucket or key")
	}
	var v []byte
	bucketByte := []byte(b)
	keyB := []byte(k)
	return v, api.db.View(func(tx *bbolt.Tx) error {
		bucket := tx.Bucket(bucketByte)
		if bucket == nil {
			return fmt.Errorf("app bucket not found")
		}
		vx := bucket.Get(keyB)
		if vx == nil {
			return fmt.Errorf("no value found for %s", k)
		}
		// An empty non-nil slice is returned nil without error.
		if len(vx) > 0 {
			v = make([]byte, len(vx))
			copy(v, vx)
		}
		return nil
	})
}
