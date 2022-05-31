package fs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/vercel/turborepo/cli/internal/turbopath"
)

func getFixture(id int) turbopath.AbsoluteSystemPath {
	cwd, _ := os.Getwd()
	root := turbopath.AbsoluteSystemPath(filepath.VolumeName(cwd) + string(os.PathSeparator))
	checking := turbopath.AbsoluteSystemPath(cwd)

	for checking != root {
		fixtureDirectory := checking.Join("fixtures")
		_, err := os.Stat(fixtureDirectory.ToString())
		if !errors.Is(err, os.ErrNotExist) {
			// Found the fixture directory!
			files, _ := os.ReadDir(fixtureDirectory.ToString())

			// Grab the specified fixture.
			for _, file := range files {
				fileName := turbopath.RelativeSystemPath(file.Name())
				if strings.Index(fileName.ToString(), fmt.Sprintf("%02d-", id)) == 0 {
					return turbopath.AbsoluteSystemPath(fixtureDirectory.Join(fileName))
				}
			}
		}
		checking = checking.Join("..")
	}

	panic("fixtures not found!")
}

func TestSpecialCharacters(t *testing.T) {
	if runtime.GOOS == "windows" {
		return
	}

	fixturePath := getFixture(1)
	newlinePath := turbopath.RelativeSystemPath("new\nline")
	quotePath := turbopath.RelativeSystemPath("\"quote\"")
	newline := fixturePath.Join(newlinePath)
	quote := fixturePath.Join(quotePath)

	// Setup
	one := os.WriteFile(newline.ToString(), []byte{}, 0644)
	two := os.WriteFile(quote.ToString(), []byte{}, 0644)

	// Cleanup
	defer func() {
		one := os.Remove(newline.ToString())
		two := os.Remove(quote.ToString())

		if one != nil || two != nil {
			return
		}
	}()

	// Setup error check
	if one != nil || two != nil {
		return
	}

	tests := []struct {
		name        string
		rootPath    turbopath.AbsoluteSystemPath
		filesToHash []turbopath.RelativeUnixPath
		want        map[turbopath.RelativeUnixPath]string
		wantErr     bool
	}{
		{
			name:     "Quotes",
			rootPath: fixturePath,
			filesToHash: []turbopath.RelativeUnixPath{
				turbopath.RelativeUnixPath(quotePath),
			},
			want: map[turbopath.RelativeUnixPath]string{
				quotePath.ToRelativeUnixPath(): "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391",
			},
		},
		{
			name:     "Newlines",
			rootPath: fixturePath,
			filesToHash: []turbopath.RelativeUnixPath{
				turbopath.RelativeUnixPath(newlinePath),
			},
			want: map[turbopath.RelativeUnixPath]string{
				newlinePath.ToRelativeUnixPath(): "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := gitHashObject(tt.rootPath, tt.filesToHash)
			if (err != nil) != tt.wantErr {
				t.Errorf("gitHashObject() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("gitHashObject() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_gitHashObject(t *testing.T) {
	fixturePath := getFixture(1)
	traversePath, err := getTraversePath(fixturePath)
	if err != nil {
		return
	}

	tests := []struct {
		name        string
		rootPath    turbopath.AbsoluteSystemPath
		filesToHash []turbopath.RelativeUnixPath
		want        map[turbopath.RelativeUnixPath]string
		wantErr     bool
	}{
		{
			name:        "No paths",
			rootPath:    fixturePath,
			filesToHash: []turbopath.RelativeUnixPath{},
			want:        map[turbopath.RelativeUnixPath]string{},
		},
		{
			name:     "Absolute paths come back relative to rootPath",
			rootPath: fixturePath.Join("child"),
			filesToHash: []turbopath.RelativeUnixPath{
				turbopath.RelativeUnixPath("../root.json"),
				turbopath.RelativeUnixPath("child.json"),
				turbopath.RelativeUnixPath("grandchild/grandchild.json"),
			},
			want: map[turbopath.RelativeUnixPath]string{
				"../root.json":               "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391",
				"child.json":                 "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391",
				"grandchild/grandchild.json": "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391",
			},
		},
		{
			name:     "Traverse outside of the repo",
			rootPath: fixturePath.Join(traversePath.ToRelativeSystemPath()).Join(".."),
			filesToHash: []turbopath.RelativeUnixPath{
				turbopath.RelativeUnixPath("null.json"),
			},
			want:    nil,
			wantErr: true,
		},
		{
			name:     "Nonexistent file",
			rootPath: fixturePath,
			filesToHash: []turbopath.RelativeUnixPath{
				turbopath.RelativeUnixPath("nonexistent.json"),
			},
			want:    nil,
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := gitHashObject(tt.rootPath, tt.filesToHash)
			if (err != nil) != tt.wantErr {
				t.Errorf("gitHashObject() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("gitHashObject() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_getTraversePath(t *testing.T) {
	fixturePath := getFixture(1)

	type args struct {
	}
	tests := []struct {
		name     string
		rootPath turbopath.AbsolutePathInterface
		want     turbopath.RelativeUnixPath
		wantErr  bool
	}{
		{
			name:     "From fixture location",
			rootPath: fixturePath,
			want:     turbopath.RelativeUnixPath("../../"),
			wantErr:  false,
		},
		{
			name:     "Traverse out of git repo",
			rootPath: fixturePath.Join("..", "..", ".."),
			want:     "",
			wantErr:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := getTraversePath(tt.rootPath)
			if (err != nil) != tt.wantErr {
				t.Errorf("getTraversePath() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("getTraversePath() = %v, want %v", got, tt.want)
			}
		})
	}
}
