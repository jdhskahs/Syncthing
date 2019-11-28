// Copyright (C) 2017 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package main

import (
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRotatedFile(t *testing.T) {
	// Verify that log rotation happens.

	dir, err := ioutil.TempDir("", "syncthing")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	open := func(name string) (io.WriteCloser, error) {
		return os.Create(name)
	}

	logName := filepath.Join(dir, "log.txt")
	testData := []byte("12345678\n")
	maxSize := int64(len(testData) + len(testData)/2)

	// We allow the log file plus two rotated copies, with zero stat interval
	// (every write is checked against size).
	rf := newRotatedFile(logName, open, maxSize, 2, 0)

	// Write some bytes.
	if _, err := rf.Write(testData); err != nil {
		t.Fatal(err)
	}
	// They should be in the log.
	checkSize(t, logName, len(testData))
	checkNotExist(t, logName+".0")

	// Write some more bytes. We should rotate and write into a new file as the
	// new bytes don't fit.
	if _, err := rf.Write(testData); err != nil {
		t.Fatal(err)
	}
	checkSize(t, logName, len(testData))
	checkSize(t, numberedFile(logName, 0), len(testData))
	checkNotExist(t, logName+".1")

	// Write another byte. That should fit without causing an extra rotate.
	_, _ = rf.Write([]byte{42})
	checkSize(t, logName, len(testData)+1)
	checkSize(t, numberedFile(logName, 0), len(testData))
	checkNotExist(t, numberedFile(logName, 1))

	// Write some more bytes. We should rotate and write into a new file as the
	// new bytes don't fit.
	if _, err := rf.Write(testData); err != nil {
		t.Fatal(err)
	}
	checkSize(t, logName, len(testData))
	checkSize(t, numberedFile(logName, 0), len(testData)+1) // the one we wrote extra to, now rotated
	checkSize(t, numberedFile(logName, 1), len(testData))
	checkNotExist(t, numberedFile(logName, 2))

	// Write some more bytes. We should rotate and write into a new file as the
	// new bytes don't fit.
	if _, err := rf.Write(testData); err != nil {
		t.Fatal(err)
	}
	checkSize(t, logName, len(testData))
	checkSize(t, numberedFile(logName, 0), len(testData))
	checkSize(t, numberedFile(logName, 1), len(testData)+1)
	checkNotExist(t, numberedFile(logName, 2)) // exceeds maxFiles so deleted
}

func TestRotatedFileStatInterval(t *testing.T) {
	dir, err := ioutil.TempDir("", "syncthing")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	open := func(name string) (io.WriteCloser, error) {
		return os.Create(name)
	}

	logName := filepath.Join(dir, "log.txt")
	testData := []byte("12345678\n")
	maxSize := int64(len(testData) + len(testData)/2)

	// A log file to hold maxSize, but only check once a minute so allows some
	// elasticity in the size.
	rf := newRotatedFile(logName, open, maxSize, 2, time.Minute)

	// Write some bytes.
	for i := 0; i < 10; i++ {
		if _, err := rf.Write(testData); err != nil {
			t.Fatal(err)
		}
	}

	// They should be in the log, which should not have had time to rotate.
	checkSize(t, logName, 10*len(testData))
	checkNotExist(t, numberedFile(logName, 0))
}

func TestNumberedFile(t *testing.T) {
	// Mostly just illustrates where the number ends up and makes sure it
	// doesn't crash without an extension.

	cases := []struct {
		in  string
		num int
		out string
	}{
		{
			in:  "syncthing.log",
			num: 42,
			out: "syncthing.42.log",
		},
		{
			in:  filepath.Join("asdfasdf", "syncthing.log.txt"),
			num: 42,
			out: filepath.Join("asdfasdf", "syncthing.log.42.txt"),
		},
		{
			in:  "syncthing-log",
			num: 42,
			out: "syncthing-log.42",
		},
	}

	for _, tc := range cases {
		res := numberedFile(tc.in, tc.num)
		if res != tc.out {
			t.Errorf("numberedFile(%q, %d) => %q, expected %q", tc.in, tc.num, res, tc.out)
		}
	}
}

func checkSize(t *testing.T, name string, size int) {
	t.Helper()
	info, err := os.Lstat(name)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != int64(size) {
		t.Errorf("%s wrong size: %d != expected %d", name, info.Size(), size)
	}
}

func checkNotExist(t *testing.T, name string) {
	t.Helper()
	_, err := os.Lstat(name)
	if !os.IsNotExist(err) {
		t.Errorf("%s should not exist", name)
	}
}

func TestAutoClosedFile(t *testing.T) {
	os.RemoveAll("_autoclose")
	defer os.RemoveAll("_autoclose")
	os.Mkdir("_autoclose", 0755)
	file := filepath.FromSlash("_autoclose/tmp")
	data := []byte("hello, world\n")

	// An autoclosed file that closes very quickly
	ac := newAutoclosedFile(file, time.Millisecond, time.Millisecond)

	// Write some data.
	if _, err := ac.Write(data); err != nil {
		t.Fatal(err)
	}

	// Wait for it to close
	start := time.Now()
	for {
		time.Sleep(time.Millisecond)
		ac.mut.Lock()
		fd := ac.fd
		ac.mut.Unlock()
		if fd == nil {
			break
		}
		if time.Since(start) > time.Second {
			t.Fatal("File should have been closed after first write")
		}
	}

	// Write more data, which should be an append.
	if _, err := ac.Write(data); err != nil {
		t.Fatal(err)
	}

	// Close.
	if err := ac.Close(); err != nil {
		t.Fatal(err)
	}

	// The file should have both writes in it.
	bs, err := ioutil.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) != 2*len(data) {
		t.Fatalf("Writes failed, expected %d bytes, not %d", 2*len(data), len(bs))
	}

	// Open the file again.
	ac = newAutoclosedFile(file, time.Second, time.Second)

	// Write something
	if _, err := ac.Write(data); err != nil {
		t.Fatal(err)
	}

	// It should now contain only one write, because the first open
	// should be a truncate.
	bs, err = ioutil.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if len(bs) != len(data) {
		t.Fatalf("Write failed, expected %d bytes, not %d", len(data), len(bs))
	}

	// Close.
	if err := ac.Close(); err != nil {
		t.Fatal(err)
	}
}
