// Adapted from https://github.com/thought-machine/please
// Copyright Thought Machine, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0
package cache

import (
	"sync"

	"github.com/vercel/turborepo/cli/internal/config"
	"github.com/vercel/turborepo/cli/internal/fs"
)

// An asyncCache is a wrapper around a Cache interface that handles incoming
// store requests asynchronously and attempts to return immediately.
// The requests are handled on an internal queue, if that fills up then
// incoming requests will start to block again until it empties.
// Retrieval requests are still handled synchronously.
type asyncCache struct {
	requests  chan cacheRequest
	realCache Cache
	wg        sync.WaitGroup
}

// A cacheRequest models an incoming cache request on our queue.
type cacheRequest struct {
	root     fs.AbsolutePath
	key      string
	duration int
	files    []fs.AbsolutePath
}

func newAsyncCache(realCache Cache, config *config.Config) Cache {
	c := &asyncCache{
		requests:  make(chan cacheRequest),
		realCache: realCache,
	}
	c.wg.Add(config.Cache.Workers)
	for i := 0; i < config.Cache.Workers; i++ {
		go c.run()
	}
	return c
}

func (c *asyncCache) Put(root fs.AbsolutePath, key string, duration int, files []fs.AbsolutePath) error {
	c.requests <- cacheRequest{
		root:     root,
		key:      key,
		files:    files,
		duration: duration,
	}
	return nil
}

func (c *asyncCache) Fetch(root fs.AbsolutePath, key string) (bool, []fs.AbsolutePath, int, error) {
	return c.realCache.Fetch(root, key)
}

func (c *asyncCache) Clean(target string) {
	c.realCache.Clean(target)
}

func (c *asyncCache) CleanAll() {
	c.realCache.CleanAll()
}

func (c *asyncCache) Shutdown() {
	// fmt.Println("Shutting down cache workers...")
	close(c.requests)
	c.wg.Wait()
	// fmt.Println("Shut down all cache workers")
}

// run implements the actual async logic.
func (c *asyncCache) run() {
	for r := range c.requests {
		c.realCache.Put(r.root, r.key, r.duration, r.files)
	}
	c.wg.Done()
}
