// Copyright (C) 2016 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package fs

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"runtime"
)

func init() {
	if runtime.GOOS == "windows" {
		// Windows has no umask so we must chose a safer set of bits to
		// begin with.
		DefaultDirPerm = 0700
	}
}

// The Filesystem interface abstracts access to the file system.
type Filesystem interface {
	Chmod(name string, mode FileMode) error
	Chtimes(name string, atime time.Time, mtime time.Time) error
	Create(name string) (File, error)
	CreateSymlink(name, target string) error
	DirNames(name string) ([]string, error)
	Lstat(name string) (FileInfo, error)
	Mkdir(name string, perm FileMode) error
	MkdirAll(name string, perm FileMode) error
	Open(name string) (File, error)
	OpenFile(name string, flags int, mode FileMode) (File, error)
	ReadSymlink(name string) (string, error)
	Remove(name string) error
	RemoveAll(name string) error
	Rename(oldname, newname string) error
	Stat(name string) (FileInfo, error)
	SymlinksSupported() bool
	Walk(name string, walkFn WalkFunc) error
	Watch(path string, ignore Matcher, ctx context.Context, ignorePerms bool) (<-chan Event, error)
	Hide(name string) error
	Unhide(name string) error
	Glob(pattern string) ([]string, error)
	Roots() ([]string, error)
	Usage(name string) (Usage, error)
	Match(matcher Matcher, name string) (MatchResult, error)
	Type() FilesystemType
	URI() string
}

// The File interface abstracts access to a regular file, being a somewhat
// smaller interface than os.File
type File interface {
	io.Closer
	io.Reader
	io.ReaderAt
	io.Writer
	io.WriterAt
	Name() string
	Truncate(size int64) error
	Stat() (FileInfo, error)
	Sync() error
}

// The FileInfo interface is almost the same as os.FileInfo, but with the
// Sys method removed (as we don't want to expose whatever is underlying)
// and with a couple of convenience methods added.
type FileInfo interface {
	// Standard things present in os.FileInfo
	Name() string
	Mode() FileMode
	Size() int64
	ModTime() time.Time
	IsDir() bool
	// Extensions
	IsRegular() bool
	IsSymlink() bool
}

// FileMode is similar to os.FileMode
type FileMode uint32

// Usage represents filesystem space usage
type Usage struct {
	Free  int64
	Total int64
}

type Event struct {
	Name string
	Type EventType
}

type EventType int

const (
	NonRemove EventType = 1 + iota
	Remove
	Mixed // Should probably not be necessary to be used in filesystem interface implementation
)

func (evType EventType) String() string {
	switch {
	case evType == NonRemove:
		return "non-remove"
	case evType == Remove:
		return "remove"
	case evType == Mixed:
		return "mixed"
	default:
		panic("bug: Unknown event type")
	}
}

var ErrWatchNotSupported = errors.New("watching is not supported")

// Equivalents from os package.

const ModePerm = FileMode(os.ModePerm)
const ModeSetgid = FileMode(os.ModeSetgid)
const ModeSetuid = FileMode(os.ModeSetuid)
const ModeSticky = FileMode(os.ModeSticky)
const ModeSymlink = FileMode(os.ModeSymlink)
const ModeType = FileMode(os.ModeType)
const PathSeparator = os.PathSeparator
const OptAppend = os.O_APPEND
const OptCreate = os.O_CREATE
const OptExclusive = os.O_EXCL
const OptReadOnly = os.O_RDONLY
const OptReadWrite = os.O_RDWR
const OptSync = os.O_SYNC
const OptTruncate = os.O_TRUNC
const OptWriteOnly = os.O_WRONLY

// SkipDir is used as a return value from WalkFuncs to indicate that
// the directory named in the call is to be skipped. It is not returned
// as an error by any function.
var SkipDir = filepath.SkipDir

// IsExist is the equivalent of os.IsExist
var IsExist = os.IsExist

// IsNotExist is the equivalent of os.IsNotExist
var IsNotExist = os.IsNotExist

// IsPermission is the equivalent of os.IsPermission
var IsPermission = os.IsPermission

// IsPathSeparator is the equivalent of os.IsPathSeparator
var IsPathSeparator = os.IsPathSeparator

// DefaultDirPerm is default directory permissions to use when creating directories
// On Unix this will be filtered by umask, on Windows (as initialized in init)
/// we use a more conservative default.
var DefaultDirPerm = ModePerm

func NewFilesystem(fsType FilesystemType, uri string) Filesystem {
	var fs Filesystem
	switch fsType {
	case FilesystemTypeBasic:
		fs = NewWalkFilesystem(newBasicFilesystem(uri))
	case FilesystemTypeEncrypted:
		encFs, err := newEncryptedFilesystem(uri)
		if err != nil {
			fs = &errorFilesystem{
				fsType: fsType,
				uri:    uri,
				err:    err,
			}
		} else {
			fs = NewWalkFilesystem(encFs)
		}
	default:
		l.Debugln("Unknown filesystem", fsType, uri)
		fs = &errorFilesystem{
			fsType: fsType,
			uri:    uri,
			err:    errors.New("filesystem with type " + fsType.String() + " does not exist."),
		}
	}

	if l.ShouldDebug("filesystem") {
		fs = &logFilesystem{fs}
	}
	return fs
}

// IsInternal returns true if the file, as a path relative to the folder
// root, represents an internal file that should always be ignored. The file
// path must be clean (i.e., in canonical shortest form).
func IsInternal(file string) bool {
	// fs cannot import config, so we hard code .stfolder here (config.DefaultMarkerName)
	internals := []string{".stfolder", ".stignore", ".stversions"}
	pathSep := string(PathSeparator)
	for _, internal := range internals {
		if file == internal {
			return true
		}
		if strings.HasPrefix(file, internal+pathSep) {
			return true
		}
	}
	return false
}

type MatchResult interface {
	IsIgnored() bool
	IsDeletable() bool
	IsCaseFolded() bool
}

type Matcher interface {
	Match(string) MatchResult
}
