package fs

import (
	"fmt"
	"io/fs"
	"io/ioutil"
	"os"
	"path/filepath"
)

// AbsolutePath represents a platform-dependent absolute path on the filesystem,
// and is used to enfore correct path manipulation
type AbsolutePath string

func CheckedToAbsolutePath(s string) (AbsolutePath, error) {
	if filepath.IsAbs(s) {
		return AbsolutePath(s), nil
	}
	return "", fmt.Errorf("%v is not an absolute path", s)
}

func UnsafeToAbsolutePath(s string) AbsolutePath {
	return AbsolutePath(s)
}

func GetCwd() (AbsolutePath, error) {
	cwdRaw, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("invalid working directory: %w", err)
	}
	cwd, err := CheckedToAbsolutePath(cwdRaw)
	if err != nil {
		return "", fmt.Errorf("cwd is not an absolute path %v: %v", cwdRaw, err)
	}
	return cwd, nil
}

func (ap AbsolutePath) ToStringDuringMigration() string {
	return ap.asString()
}

func (ap AbsolutePath) Join(args ...string) AbsolutePath {
	return AbsolutePath(filepath.Join(ap.asString(), filepath.Join(args...)))
}

// JoinPOSIXPath appends a relative path in posix format ('/' seperator) to
// this absolute path, by first converting the input to a platform-dependent path
func (ap AbsolutePath) JoinPOSIXPath(posixPath string) AbsolutePath {
	return ap.Join(filepath.FromSlash(posixPath))
}

func (ap AbsolutePath) asString() string {
	return string(ap)
}
func (ap AbsolutePath) Dir() AbsolutePath {
	return AbsolutePath(filepath.Dir(ap.asString()))
}
func (ap AbsolutePath) MkdirAll() error {
	return os.MkdirAll(ap.asString(), DirPermissions)
}
func (ap AbsolutePath) Remove() error {
	return os.Remove(ap.asString())
}
func (ap AbsolutePath) Open() (*os.File, error) {
	return os.Open(ap.asString())
}

// OpenFile is the AbsolutePath implementation of os.OpenFile
func (ap AbsolutePath) OpenFile(flag int, mode fs.FileMode) (*os.File, error) {
	return os.OpenFile(ap.asString(), flag, mode)
}

func (ap AbsolutePath) ReadFile() ([]byte, error) {
	return ioutil.ReadFile(ap.asString())
}

// WriteFile is the AbsolutePath implementation of ioutil.WriteFile
func (ap AbsolutePath) WriteFile(bytes []byte, mode fs.FileMode) error {
	return ioutil.WriteFile(ap.asString(), bytes, mode)
}
func (ap AbsolutePath) FileExists() bool {
	return FileExists(ap.asString())
}
func (ap AbsolutePath) PathExists() bool {
	return PathExists(ap.asString())
}
func (ap AbsolutePath) EnsureDir() error {
	return EnsureDir(ap.asString())
}

// Lstat is the AbsolutePath implementation of os.Lstat
func (ap AbsolutePath) Lstat() (fs.FileInfo, error) {
	return os.Lstat(ap.asString())
}

// Readlink reads a link at this path, and returns the AbsolutePath for the target
func (ap AbsolutePath) Readlink() (AbsolutePath, error) {
	dest, err := os.Readlink(ap.asString())
	if err != nil {
		return "", err
	}
	if filepath.IsAbs(dest) {
		return AbsolutePath(dest), nil
	}
	// We know the starting point, so if it's a relative path
	// we can join
	return ap.Dir().Join(dest), nil
}

// Symlink is the AbsolutePath implementation of os.Symlink
func (ap AbsolutePath) Symlink(linkName AbsolutePath) error {
	return os.Symlink(ap.asString(), linkName.asString())
}

// Link is the AbsolutePath implementation of os.Link
func (ap AbsolutePath) Link(to AbsolutePath) error {
	return os.Link(ap.asString(), to.asString())
}

// IsDirectory is the AbsolutePath implementation of fs.IsDirectory
func (ap AbsolutePath) IsDirectory() bool {
	return IsDirectory(ap.asString())
}

// RelativePathString returns the relative path from this AbsolutePath to another
// AbsolutePath as a string.
// TODO(gsoltis): should this be RelativePathStringDuringMigration?
func (ap AbsolutePath) RelativePathString(to AbsolutePath) (string, error) {
	return filepath.Rel(ap.asString(), to.asString())
}
