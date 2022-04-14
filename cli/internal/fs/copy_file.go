// Adapted from https://github.com/thought-machine/please
// Copyright Thought Machine, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0
package fs

import (
	"errors"
	"os"

	"github.com/karrick/godirwalk"
)

// CopyOrLinkFile either copies or hardlinks a file based on the link argument.
// Falls back to a copy if link fails and fallback is true.
func CopyOrLinkFile(from, to AbsolutePath, fromMode, toMode os.FileMode, link, fallback bool) error {
	if link {
		if (fromMode & os.ModeSymlink) != 0 {
			// Don't try to hard-link to a symlink, that doesn't work reliably across all platforms.
			// Instead recreate an equivalent symlink in the new location.
			dest, err := from.Readlink()
			if err != nil {
				return err
			}
			return dest.Symlink(to)
		}
		if err := from.Link(to); err == nil || !fallback {
			return err
		}
	}
	return CopyFile(from.ToStringDuringMigration(), to.ToStringDuringMigration(), toMode)
}

// RecursiveCopy copies either a single file or a directory.
// 'mode' is the mode of the destination file.
func RecursiveCopy(from string, to string, mode os.FileMode) error {
	return RecursiveCopyOrLinkFile(UnsafeToAbsolutePath(from), UnsafeToAbsolutePath(to), mode, false, false)
}

// RecursiveCopyOrLinkFile recursively copies or links a file or directory.
// 'mode' is the mode of the destination file.
// If 'link' is true then we'll hardlink files instead of copying them.
// If 'fallback' is true then we'll fall back to a copy if linking fails.
func RecursiveCopyOrLinkFile(from AbsolutePath, to AbsolutePath, mode os.FileMode, link, fallback bool) error {
	info, err := from.Lstat()
	if err != nil {
		return err
	}
	if info.IsDir() {
		return WalkMode(from, func(name string, isDir bool, fileMode os.FileMode) error {
			absName := UnsafeToAbsolutePath(name)
			dest := to.Join(name[len(from):])
			if isDir {
				return dest.MkdirAll()
			}
			if isSame, err := sameFile(from, absName); err != nil {
				return err
			} else if isSame {
				return nil
			}
			return CopyOrLinkFile(absName, dest, fileMode, mode, link, fallback)
		})
	}
	return CopyOrLinkFile(from, to, info.Mode(), mode, link, fallback)
}

// Walk implements an equivalent to filepath.Walk.
// It's implemented over github.com/karrick/godirwalk but the provided interface doesn't use that
// to make it a little easier to handle.
func Walk(rootPath string, callback func(name string, isDir bool) error) error {
	return WalkMode(UnsafeToAbsolutePath(rootPath), func(name string, isDir bool, mode os.FileMode) error {
		return callback(name, isDir)
	})
}

// WalkMode is like Walk but the callback receives an additional type specifying the file mode type.
// N.B. This only includes the bits of the mode that determine the mode type, not the permissions.
func WalkMode(rootPath AbsolutePath, callback func(name string, isDir bool, mode os.FileMode) error) error {
	return godirwalk.Walk(rootPath.ToStringDuringMigration(), &godirwalk.Options{
		Callback: func(name string, info *godirwalk.Dirent) error {
			// currently we support symlinked files, but not symlinked directories:
			// For copying, we Mkdir and bail if we encounter a symlink to a directoy
			// For finding packages, we enumerate the symlink, but don't follow inside
			isDir, err := info.IsDirOrSymlinkToDir()
			if err != nil {
				pathErr := &os.PathError{}
				if errors.As(err, &pathErr) {
					// If we have a broken link, skip this entry
					return godirwalk.SkipThis
				}
				return err
			}
			return callback(name, isDir, info.ModeType())
		},
		ErrorCallback: func(pathname string, err error) godirwalk.ErrorAction {
			pathErr := &os.PathError{}
			if errors.As(err, &pathErr) {
				return godirwalk.SkipNode
			}
			return godirwalk.Halt
		},
		Unsorted:            true,
		AllowNonDirectory:   true,
		FollowSymbolicLinks: false,
	})
}

// sameFile returns true if the two given paths refer to the same physical
// file on disk, using the unique file identifiers from the underlying
// operating system. For example, on Unix systems this checks whether the
// two files are on the same device and have the same inode.
func sameFile(a, b AbsolutePath) (bool, error) {
	if a == b {
		return true, nil
	}

	aInfo, err := a.Lstat()
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	bInfo, err := b.Lstat()
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	return os.SameFile(aInfo, bInfo), nil
}
