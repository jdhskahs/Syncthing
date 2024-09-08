// Copyright (C) 2018 The Syncthing Authors.
//
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this file,
// You can obtain one at https://mozilla.org/MPL/2.0/.

package model

import (
	"context"
	"errors"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/syncthing/syncthing/lib/blockstorage"
	"github.com/syncthing/syncthing/lib/config"
	"github.com/syncthing/syncthing/lib/db"
	"github.com/syncthing/syncthing/lib/events"
	"github.com/syncthing/syncthing/lib/ignore"
	"github.com/syncthing/syncthing/lib/protocol"
	"github.com/syncthing/syncthing/lib/semaphore"
	"github.com/syncthing/syncthing/lib/stats"
	"github.com/syncthing/syncthing/lib/sync"
	"github.com/syncthing/syncthing/lib/versioner"
)

func init() {
	folderFactories[config.FolderTypeVirtual] = newVirtualFolder
	log.SetFlags(log.Lmicroseconds)
	log.Default().SetOutput(os.Stdout)
	log.Default().SetPrefix("TESTLOG ")
}

type virtualFolderSyncthingService struct {
	*folderBase
	blockCache   blockstorage.HashBlockStorageI
	mountPath    string
	mountService io.Closer

	backgroundDownloadPending chan struct{}
	backgroundDownloadQueue   jobQueue
}

func (vFSS *virtualFolderSyncthingService) GetBlockDataFromCacheOrDownload(
	snap *db.Snapshot,
	file protocol.FileInfo,
	block protocol.BlockInfo,
) ([]byte, bool) {
	data, ok := vFSS.blockCache.Get(block.Hash)
	if !ok {
		err := vFSS.pullBlockBase(func(blockData []byte) {
			data = blockData
		}, snap, file, block)

		if err != nil {
			return nil, false
		}

		vFSS.blockCache.Set(block.Hash, data)
	}

	return data, true
}

func newVirtualFolder(
	model *model,
	fset *db.FileSet,
	ignores *ignore.Matcher,
	cfg config.FolderConfiguration,
	ver versioner.Versioner,
	evLogger events.Logger,
	ioLimiter *semaphore.Semaphore,
) service {
	return &virtualFolderSyncthingService{
		folderBase:                newFolderBase(cfg, evLogger, model, fset),
		blockCache:                nil,
		backgroundDownloadPending: make(chan struct{}, 1),
		backgroundDownloadQueue:   *newJobQueue(),
	}
}

func (f *virtualFolderSyncthingService) RequestBackgroundDownload(filename string, size int64, modified time.Time) {
	wasNew := f.backgroundDownloadQueue.PushIfNew(filename, size, modified)
	if !wasNew {
		return
	}

	f.backgroundDownloadQueue.SortAccordingToConfig(f.Order)
	select {
	case f.backgroundDownloadPending <- struct{}{}:
	default:
	}
}

func (f *virtualFolderSyncthingService) Serve_backgroundDownloadTask() {
	for {

		select {
		case <-f.ctx.Done():
			return
		case <-f.backgroundDownloadPending:
		}

		for job, ok := f.backgroundDownloadQueue.Pop(); ok; job, ok = f.backgroundDownloadQueue.Pop() {
			func() {
				defer f.backgroundDownloadQueue.Done(job)

				snap, err := f.fset.Snapshot()
				if err != nil {
					return
				}
				fi, ok := snap.GetGlobal(job)
				if !ok {
					return
				}

				all_ok := true
				for _, bi := range fi.Blocks {
					_, ok := f.GetBlockDataFromCacheOrDownload(snap, fi, bi)
					all_ok = all_ok && ok
				}

				if !all_ok {
					return
				}

				f.fset.UpdateOne(protocol.LocalDeviceID, &fi)

				seq := f.fset.Sequence(protocol.LocalDeviceID)
				f.evLogger.Log(events.LocalIndexUpdated, map[string]interface{}{
					"folder":    f.ID,
					"items":     1,
					"filenames": append([]string(nil), fi.Name),
					"sequence":  seq,
					"version":   seq, // legacy for sequence
				})
			}()
		}
	}
}

func (f *virtualFolderSyncthingService) Serve(ctx context.Context) error {
	f.model.foldersRunning.Add(1)
	defer f.model.foldersRunning.Add(-1)

	f.ctx = ctx

	if f.blockCache == nil {
		//f.blockCache = blockstorage.NewGoCloudUrlStorage(ctx, "mem://")

		blobUrl := ""
		virtual_descriptor, hasVirtualDescriptor := strings.CutPrefix(f.Path, ":virtual:")
		if hasVirtualDescriptor {
			parts := strings.Split(virtual_descriptor, ":mount_at:")
			if len(parts) != 2 {
				return errors.New("missing \":mount_at:\" in virtual descriptor")
			}
			//url := "s3://bucket-syncthing-uli-virtual-folder-test1/" + myDir
			blobUrl = parts[0]
			f.mountPath = parts[1]
		} else {
			myDir := f.Path + "_BlobStorage"
			if err := os.MkdirAll(myDir, 0o777); err != nil {
				log.Fatal(err)
			}
			blobUrl = "file://" + myDir + "?no_tmp_dir=yes"
			f.mountPath = f.Path + "R"
		}

		f.blockCache = blockstorage.NewGoCloudUrlStorage(ctx, blobUrl)
	}

	if f.mountService == nil {
		stVF := &syncthingVirtualFolderFuseAdapter{
			vFSS:        f,
			folderID:    f.ID,
			model:       f.model,
			fset:        f.fset,
			ino_mu:      sync.NewMutex(),
			next_ino_nr: 1,
			ino_mapping: make(map[string]uint64),
		}
		mount, err := NewVirtualFolderMount(f.mountPath, f.ID, f.Label, stVF)
		if err != nil {
			return err
		}

		f.mountService = mount
	}

	backgroundDownloadTasks := 4
	for i := 0; i < backgroundDownloadTasks; i++ {
		go f.Serve_backgroundDownloadTask()
	}

	for {
		select {
		case <-ctx.Done():
			f.mountService.Close()
			return nil

		case <-f.pullScheduled:
			continue

		}
	}
}

func (f *virtualFolderSyncthingService) Override()                 {}
func (f *virtualFolderSyncthingService) Revert()                   {}
func (f *virtualFolderSyncthingService) DelayScan(d time.Duration) {}
func (vf *virtualFolderSyncthingService) ScheduleScan() {
	vf.Scan([]string{})
}
func (f *virtualFolderSyncthingService) Jobs(page, per_page int) ([]string, []string, int) {
	return f.backgroundDownloadQueue.Jobs(page, per_page)
}
func (f *virtualFolderSyncthingService) BringToFront(filename string) {
	f.backgroundDownloadQueue.BringToFront(filename)
}

func (vf *virtualFolderSyncthingService) Scan(subs []string) error {
	snap, err := vf.fset.Snapshot()
	if err != nil {
		return err
	}

	snap.WithNeedTruncated(protocol.LocalDeviceID, func(f protocol.FileIntf) bool /* true to continue */ {
		if f.IsDirectory() {
			// no work to do for directories. directly take over:
			fi, ok := snap.GetGlobal(f.FileName())
			if ok {
				vf.fset.UpdateOne(protocol.LocalDeviceID, &fi)
			}
		} else {
			vf.RequestBackgroundDownload(f.FileName(), f.FileSize(), f.ModTime())
		}
		return true
	})

	return nil
}
func (f *virtualFolderSyncthingService) Errors() []FileError             { return []FileError{} }
func (f *virtualFolderSyncthingService) WatchError() error               { return nil }
func (f *virtualFolderSyncthingService) ScheduleForceRescan(path string) {}
func (f *virtualFolderSyncthingService) GetStatistics() (stats.FolderStatistics, error) {
	return stats.FolderStatistics{}, nil
}

var _ = (virtualFolderServiceI)((*virtualFolderSyncthingService)(nil))

func (vf *virtualFolderSyncthingService) GetHashBlockData(hash []byte, response_data []byte) (int, error) {
	data, ok := vf.blockCache.Get(hash)
	if !ok {
		return 0, protocol.ErrNoSuchFile
	}
	n := copy(response_data, data)
	return n, nil
}