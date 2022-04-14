// Adapted from https://github.com/thought-machine/please
// Copyright Thought Machine, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0
package cache

import (
	"encoding/json"
	"fmt"
	"runtime"

	"github.com/vercel/turborepo/cli/internal/analytics"
	"github.com/vercel/turborepo/cli/internal/config"
	"github.com/vercel/turborepo/cli/internal/fs"
	"golang.org/x/sync/errgroup"
)

// fsCache is a local filesystem cache
type fsCache struct {
	cacheDirectory fs.AbsolutePath
	recorder       analytics.Recorder
}

// newFsCache creates a new filesystem cache
func newFsCache(config *config.Config, recorder analytics.Recorder) Cache {
	return &fsCache{cacheDirectory: config.Cache.Dir, recorder: recorder}
}

// Fetch returns true if items are cached. It moves them into position as a side effect.
func (f *fsCache) Fetch(root fs.AbsolutePath, hash string) (bool, []fs.AbsolutePath, int, error) {
	cachedFolder := f.cacheDirectory.Join(hash)

	// If it's not in the cache bail now
	if !cachedFolder.PathExists() {
		f.logFetch(false, hash, 0)
		return false, nil, 0, nil
	}

	// Otherwise, copy it into position
	err := fs.RecursiveCopyOrLinkFile(cachedFolder, root, fs.DirPermissions, true, true)
	if err != nil {
		// TODO: what event to log here?
		return false, nil, 0, fmt.Errorf("error moving artifact from cache into %v: %w", root, err)
	}

	meta, err := readCacheMetaFile(f.cacheDirectory.Join(hash + "-meta.json"))
	if err != nil {
		return false, nil, 0, fmt.Errorf("error reading cache metadata: %w", err)
	}
	f.logFetch(true, hash, meta.Duration)
	return true, nil, meta.Duration, nil
}

func (f *fsCache) logFetch(hit bool, hash string, duration int) {
	var event string
	if hit {
		event = cacheEventHit
	} else {
		event = cacheEventMiss
	}
	payload := &CacheEvent{
		Source:   "LOCAL",
		Event:    event,
		Hash:     hash,
		Duration: duration,
	}
	f.recorder.LogEvent(payload)
}

func (f *fsCache) Put(root fs.AbsolutePath, hash string, duration int, files []fs.AbsolutePath) error {
	g := new(errgroup.Group)

	numDigesters := runtime.NumCPU()
	fileQueue := make(chan fs.AbsolutePath, numDigesters)

	for i := 0; i < numDigesters; i++ {
		g.Go(func() error {
			for file := range fileQueue {
				if !file.IsDirectory() {
					relativePath, err := root.RelativePathString(file)
					if err != nil {
						return fmt.Errorf("error getting relative path from %v to cache artifact %v: %v", root, file, err)
					}
					artifactPath := f.cacheDirectory.Join(hash, relativePath)
					if err := artifactPath.EnsureDir(); err != nil {
						return fmt.Errorf("error ensuring directory file from cache: %w", err)
					}

					if err := fs.CopyOrLinkFile(file, artifactPath, fs.DirPermissions, fs.DirPermissions, true, true); err != nil {
						return fmt.Errorf("error copying file from cache: %w", err)
					}
				}
			}
			return nil
		})
	}

	for _, file := range files {
		fileQueue <- file
	}
	close(fileQueue)

	if err := g.Wait(); err != nil {
		return err
	}

	writeCacheMetaFile(f.cacheDirectory.Join(hash+"-meta.json"), &CacheMetadata{
		Duration: duration,
		Hash:     hash,
	})

	return nil
}

func (f *fsCache) Clean(target string) {
	fmt.Println("Not implemented yet")
}

func (f *fsCache) CleanAll() {
	fmt.Println("Not implemented yet")
}

func (cache *fsCache) Shutdown() {}

// CacheMetadata stores duration and hash information for a cache entry so that aggregate Time Saved calculations
// can be made from artifacts from various caches
type CacheMetadata struct {
	Hash     string `json:"hash"`
	Duration int    `json:"duration"`
}

// writeCacheMetaFile writes cache metadata file at a path
func writeCacheMetaFile(path fs.AbsolutePath, config *CacheMetadata) error {
	jsonBytes, marshalErr := json.Marshal(config)
	if marshalErr != nil {
		return marshalErr
	}
	writeFilErr := path.WriteFile(jsonBytes, 0644)
	if writeFilErr != nil {
		return writeFilErr
	}
	return nil
}

// readCacheMetaFile reads cache metadata file at a path
func readCacheMetaFile(path fs.AbsolutePath) (*CacheMetadata, error) {
	jsonBytes, readFileErr := path.ReadFile()
	if readFileErr != nil {
		return nil, readFileErr
	}
	var config CacheMetadata
	marshalErr := json.Unmarshal(jsonBytes, &config)
	if marshalErr != nil {
		return nil, marshalErr
	}
	return &config, nil
}
