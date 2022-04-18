// Adapted from https://github.com/thought-machine/please
// Copyright Thought Machine, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0
package cache

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	log "log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/vercel/turborepo/cli/internal/analytics"
	"github.com/vercel/turborepo/cli/internal/config"
	"github.com/vercel/turborepo/cli/internal/fs"
)

type httpCache struct {
	writable       bool
	config         *config.Config
	requestLimiter limiter
	recorder       analytics.Recorder
	signerVerifier *ArtifactSignatureAuthentication
}

type limiter chan struct{}

func (l limiter) acquire() {
	l <- struct{}{}
}

func (l limiter) release() {
	<-l
}

// mtime is the time we attach for the modification time of all files.
var mtime = time.Date(2000, time.January, 1, 0, 0, 0, 0, time.UTC)

// nobody is the usual uid / gid of the 'nobody' user.
const nobody = 65534

func (cache *httpCache) Put(root fs.AbsolutePath, hash string, duration int, files []fs.AbsolutePath) error {
	// if cache.writable {
	cache.requestLimiter.acquire()
	defer cache.requestLimiter.release()

	r, w := io.Pipe()
	go cache.write(w, root, hash, files)

	// Read the entire aritfact tar into memory so we can easily compute the signature.
	// Note: retryablehttp.NewRequest reads the files into memory anyways so there's no
	// additional overhead by doing the ioutil.ReadAll here instead.
	artifactBody, err := ioutil.ReadAll(r)
	if err != nil {
		return fmt.Errorf("failed to store files in HTTP cache: %w", err)
	}
	tag := ""
	if cache.signerVerifier.isEnabled() {
		tag, err = cache.signerVerifier.generateTag(hash, artifactBody)
		if err != nil {
			return fmt.Errorf("failed to store files in HTTP cache: %w", err)
		}
	}
	return cache.config.ApiClient.PutArtifact(hash, artifactBody, duration, tag)
}

// write writes a series of files into the given Writer.
func (cache *httpCache) write(w io.WriteCloser, root fs.AbsolutePath, hash string, files []fs.AbsolutePath) {
	defer w.Close()
	gzw := gzip.NewWriter(w)
	defer gzw.Close()
	tw := tar.NewWriter(gzw)
	defer tw.Close()
	for _, file := range files {
		// log.Printf("caching file %v", file)
		if err := cache.storeFile(tw, root, file); err != nil {
			log.Printf("[ERROR] Error uploading artifacts to HTTP cache: %s", err)
			// TODO(jaredpalmer): How can we cancel the request at this point?
		}
	}
}

func (cache *httpCache) storeFile(tw *tar.Writer, root fs.AbsolutePath, name fs.AbsolutePath) error {
	info, err := name.Lstat()
	if err != nil {
		return err
	}
	target := ""
	if info.Mode()&os.ModeSymlink != 0 {
		linkTarget, err := name.Readlink()
		if err != nil {
			return err
		}
		target = linkTarget
	}
	hdr, err := tar.FileInfoHeader(info, filepath.ToSlash(target))
	if err != nil {
		return err
	}
	repoRelativePath, err := root.RelativePathString(name)
	if err != nil {
		return err
	}
	// Ensure posix path for filename written in header.
	hdr.Name = filepath.ToSlash(repoRelativePath)
	// Zero out all timestamps.
	hdr.ModTime = mtime
	hdr.AccessTime = mtime
	hdr.ChangeTime = mtime
	// Strip user/group ids.
	hdr.Uid = nobody
	hdr.Gid = nobody
	hdr.Uname = "nobody"
	hdr.Gname = "nobody"
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	} else if info.IsDir() || target != "" {
		return nil // nothing to write
	}
	f, err := name.Open()
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(tw, f)
	return err
}

func (cache *httpCache) Fetch(root fs.AbsolutePath, hash string) (bool, []fs.AbsolutePath, int, error) {
	cache.requestLimiter.acquire()
	defer cache.requestLimiter.release()
	hit, files, duration, err := cache.retrieve(root, hash)
	if err != nil {
		// TODO: analytics event?
		return false, files, duration, fmt.Errorf("failed to retrieve files from HTTP cache: %w", err)
	}
	cache.logFetch(hit, hash, duration)
	return hit, files, duration, err
}

func (cache *httpCache) logFetch(hit bool, hash string, duration int) {
	var event string
	if hit {
		event = cacheEventHit
	} else {
		event = cacheEventMiss
	}
	payload := &CacheEvent{
		Source:   "REMOTE",
		Event:    event,
		Hash:     hash,
		Duration: duration,
	}
	cache.recorder.LogEvent(payload)
}

func (cache *httpCache) retrieve(root fs.AbsolutePath, hash string) (bool, []fs.AbsolutePath, int, error) {
	resp, err := cache.config.ApiClient.FetchArtifact(hash, nil)
	if err != nil {
		return false, nil, 0, err
	}
	defer resp.Body.Close()
	duration := 0
	// If present, extract the duration from the response.
	if resp.Header.Get("x-artifact-duration") != "" {
		intVar, err := strconv.Atoi(resp.Header.Get("x-artifact-duration"))
		if err != nil {
			return false, nil, 0, fmt.Errorf("invalid x-artifact-duration header: %w", err)
		}
		duration = intVar
	}
	if resp.StatusCode == http.StatusNotFound {
		return false, nil, 0, nil // doesn't exist - not an error
	} else if resp.StatusCode != http.StatusOK {
		b, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			return false, nil, 0, fmt.Errorf("failed to read remote cache %v response body: %v", resp.StatusCode, err)
		}
		return false, nil, 0, fmt.Errorf("%s", string(b))
	}
	artifactReader := resp.Body
	if cache.signerVerifier.isEnabled() {
		expectedTag := resp.Header.Get("x-artifact-tag")
		if expectedTag == "" {
			// If the verifier is enabled all incoming artifact downloads must have a signature
			return false, nil, 0, errors.New("artifact verification failed: Downloaded artifact is missing required x-artifact-tag header")
		}
		b, _ := ioutil.ReadAll(artifactReader)
		if err != nil {
			return false, nil, 0, fmt.Errorf("artifact verifcation failed: %w", err)
		}
		isValid, err := cache.signerVerifier.validate(hash, b, expectedTag)
		if err != nil {
			return false, nil, 0, fmt.Errorf("artifact verifcation failed: %w", err)
		}
		if !isValid {
			err = fmt.Errorf("artifact verification failed: artifact tag does not match expected tag %s", expectedTag)
			return false, nil, 0, err
		}
		// The artifact has been verified and the body can be read and untarred
		artifactReader = ioutil.NopCloser(bytes.NewReader(b))
	}
	gzr, err := gzip.NewReader(artifactReader)
	if err != nil {
		return false, nil, 0, err
	}
	defer gzr.Close()
	files := []fs.AbsolutePath{}
	missingLinks := []*tar.Header{}
	tr := tar.NewReader(gzr)
	for {
		hdr, err := tr.Next()
		if err != nil {
			if err == io.EOF {
				for _, link := range missingLinks {
					err := restoreSymlink(root, link, false)
					if err != nil {
						return false, nil, 0, err
					}
					// linkTarget := root.JoinPOSIXPath(link.Name)
					// linkName := root.JoinPOSIXPath(link.Linkname)
					// if err := linkTarget.Symlink(linkName); err != nil {
					// 	return false, nil, 0, err
					// }
				}

				return true, files, duration, nil
			}
			return false, nil, 0, err
		}
		localPath := root.JoinPOSIXPath(hdr.Name)
		// Note that hdr.Name should not be used below here. It is
		// a repo-relative posix path. localPath is a platform-dependent
		// absolute path for the file / directory / link we're creating
		files = append(files, localPath)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := localPath.MkdirAll(); err != nil {
				return false, nil, 0, err
			}
		case tar.TypeReg:
			err := localPath.EnsureDir()
			if err != nil {
				return false, nil, 0, err
			}
			if f, err := localPath.OpenFile(os.O_WRONLY|os.O_TRUNC|os.O_CREATE, os.FileMode(hdr.Mode)); err != nil {
				return false, nil, 0, err
			} else if _, err := io.Copy(f, tr); err != nil {
				return false, nil, 0, err
			} else if err := f.Close(); err != nil {
				return false, nil, 0, err
			}
		case tar.TypeSymlink:
			if err := restoreSymlink(root, hdr, true); errors.Is(err, errNonexistentLinkTarget) {
				// The target we're linking to doesn't exist. It might exist later
				// so try again once we've read the whole tar
				missingLinks = append(missingLinks, hdr)
			} else if err != nil {
				return false, nil, 0, err
			}
		default:
			log.Printf("Unhandled file type %d for %s", hdr.Typeflag, hdr.Name)
		}
	}
}

var errNonexistentLinkTarget = errors.New("the link target does not exist")

func restoreSymlink(root fs.AbsolutePath, hdr *tar.Header, allowNonexistentTargets bool) error {
	// Note that hdr.Linkname is really the link target
	linkTarget := filepath.FromSlash(hdr.Linkname)
	localLinkFilename := root.JoinPOSIXPath(hdr.Name)
	localLinkTarget := root.JoinPOSIXPath(hdr.Linkname)
	err := localLinkFilename.EnsureDir()
	if err != nil {
		return err
	}
	if _, err := localLinkTarget.Lstat(); err != nil {
		if os.IsNotExist(err) {
			if !allowNonexistentTargets {
				return errNonexistentLinkTarget
			}
			// // The target we're linking to doesn't exist. It might exist later
			// // so try again once we've read the whole tar
			// missingLinks = append(missingLinks, hdr)
			// continue
		} else {
			return err
		}
	}
	// Ensure that the link we're about to create doesn't already exist
	if localLinkFilename.FileExists() {
		if err := localLinkFilename.Remove(); err != nil {
			return err
		}
	}
	if err := localLinkFilename.SymlinkTo(linkTarget); err != nil {
		return err
	}
	return nil
}

func (cache *httpCache) Clean(target string) {
	// Not possible; this implementation can only clean for a hash.
}

func (cache *httpCache) CleanAll() {
	// Also not possible.
}

func (cache *httpCache) Shutdown() {}

func newHTTPCache(config *config.Config, recorder analytics.Recorder) *httpCache {
	return &httpCache{
		writable:       true,
		config:         config,
		requestLimiter: make(limiter, 20),
		recorder:       recorder,
		signerVerifier: &ArtifactSignatureAuthentication{
			// TODO(Gaspar): this should use RemoteCacheOptions.TeamId once we start
			// enforcing team restrictions for repositories.
			teamId:  config.TeamId,
			enabled: config.TurboConfigJSON.RemoteCacheOptions.Signature,
		},
	}
}
