// Copyright (c) 2015 HPE Software Inc. All rights reserved.
// Copyright (c) 2013 ActiveState Software Inc. All rights reserved.

package watch

import (
	"fmt"
	"github.com/hpcloud/tail/util"
	"gopkg.in/fsnotify.v1"
	"gopkg.in/tomb.v1"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// InotifyFileWatcher uses inotify to monitor file changes.
type InotifyFileWatcher struct {
	Filename string
	Size     int64
}

var (
	wg sync.WaitGroup
)

func NewInotifyFileWatcher(filename string) *InotifyFileWatcher {
	fw := &InotifyFileWatcher{filepath.Clean(filename), 0}
	return fw
}

func (fw *InotifyFileWatcher) BlockUntilExists(t *tomb.Tomb) error {
	err := WatchCreate(fw.Filename)
	if err != nil {
		return err
	}
	defer RemoveWatchCreate(fw.Filename)

	// Do a real check now as the file might have been created before
	// calling `WatchFlags` above.
	if _, err = os.Stat(fw.Filename); !os.IsNotExist(err) {
		// file exists, or stat returned an error.
		return err
	}

	events := Events(fw.Filename)

	for {
		select {
		case evt, ok := <-events:
			if !ok {
				return fmt.Errorf("inotify watcher has been closed")
			}
			evtName, err := filepath.Abs(evt.Name)
			if err != nil {
				return err
			}
			fwFilename, err := filepath.Abs(fw.Filename)
			if err != nil {
				return err
			}
			if evtName == fwFilename {
				return nil
			}
		case <-t.Dying():
			return tomb.ErrDying
		}
	}
	panic("unreachable")
}

func (fw *InotifyFileWatcher) ChangeEvents(t *tomb.Tomb, pos int64) (*FileChanges, error) {
	err := Watch(fw.Filename)
	if err != nil {
		return nil, err
	}

	changes := NewFileChanges()
	fw.Size = pos

	stopPoll := make(chan struct{}, 1)

	// Polling func for SymLinkChange
	go func() {

		fileInfo, err := os.Lstat(fw.Filename)
		if err != nil {
			log.Println("Error: Unable to open file: ", err.Error())
			return
		}

		// Return if not a symlink
		if fileInfo.Mode()&os.ModeSymlink != os.ModeSymlink {
			return
		}

		// wg.Add(1)
		// defer wg.Done()

		for {
			select {
			case <-stopPoll:
				log.Println("read from stopPoll")
				return
			default:

				targetOld, err := os.Readlink(fw.Filename)
				if err != nil {
					log.Println("Readlink old:", fw.Filename, err.Error())
					<-time.After(100 * time.Millisecond)
					continue
				}

				<-time.After(1 * time.Second)

				targetNew, err := os.Readlink(fw.Filename)
				if err != nil {
					log.Println("Readlink new:", fw.Filename, err.Error())
					<-time.After(5 * time.Second)
					continue
				}

				// send to event channel if symlink target is changed
				if targetNew != targetOld {
					changes.NotifySymLinkChanged()
				}

			}

		}
	}()

	// Event notification func for other changes
	go func() {

		defer func() {
			RemoveWatch(fw.Filename)
			log.Println("RemoveWatch(fw.Filename) done. Waiting to insert to struct")
			log.Println("len:", len(stopPoll), "cap:", cap(stopPoll))
			stopPoll <- struct{}{}
			log.Println("inserted to struct")
			t.Done()
		}()

		events := Events(fw.Filename)

		for {
			prevSize := fw.Size

			var evt fsnotify.Event
			var ok bool

			select {
			case evt, ok = <-events:
				if !ok {
					return
				}
				break
			case <-t.Dying():
				log.Println("<-t.Dying() in inotify")
				return
			}

			switch {
			case evt.Op&fsnotify.Remove == fsnotify.Remove:
				fallthrough

			case evt.Op&fsnotify.Rename == fsnotify.Rename:
				changes.NotifyDeleted()
				return

			case evt.Op&fsnotify.Write == fsnotify.Write:
				fi, err := os.Stat(fw.Filename)
				if err != nil {
					if os.IsNotExist(err) {
						changes.NotifyDeleted()
						return
					}
					// XXX: report this error back to the user
					util.Fatal("Failed to stat file %v: %v", fw.Filename, err)
				}
				fw.Size = fi.Size()

				if prevSize > 0 && prevSize > fw.Size {
					changes.NotifyTruncated()
				} else {
					changes.NotifyModified()
				}
				prevSize = fw.Size

			}
		}
	}()

	return changes, nil
}
