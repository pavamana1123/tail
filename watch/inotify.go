// Copyright (c) 2015 HPE Software Inc. All rights reserved.
// Copyright (c) 2013 ActiveState Software Inc. All rights reserved.

package watch

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"strings"

	"github.com/hpcloud/tail/util"
	"gopkg.in/fsnotify.v1"
	"gopkg.in/tomb.v1"
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
		log.Println("WatchCreate", err.Error())
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
	// polling for changes in symlink target, if symlink exists
	// if path does not contain symlinks, the go routine stops
	go changes.detectSymlinkChanges(t, fw)
	// detecting file events other than symlink change.
	go changes.detectInotifyChanges(t, fw)

	return changes, nil
}

func (changes *FileChanges) detectInotifyChanges(t *tomb.Tomb, fw *InotifyFileWatcher) {

	defer RemoveWatch(fw.Filename)

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
}

func (changes *FileChanges) detectSymlinkChanges(t *tomb.Tomb, fw *InotifyFileWatcher) {

	isSymLink, symlinkPath := getSymlinkPath(fw.Filename)

	if !isSymLink {
		return
	}

retry:
	target, err := os.Readlink(symlinkPath)
	if err != nil {
		log.Println("Readlink:", symlinkPath, err.Error())
		select {
		case <-time.After(1 * time.Second):
			goto retry
		case <-t.Dying():
			return
		}

	}

	changes.pollSymlinkForChange(t, symlinkPath, target)

}

func (changes *FileChanges) pollSymlinkForChange(t *tomb.Tomb, symlinkPath, targetOld string) {

	var (
		target string
		err    error
	)

	for {
		select {
		case <-t.Dying():
			return
		default:
			target, err = os.Readlink(symlinkPath)
			if err != nil {
				log.Println("Readlink new:", symlinkPath, err.Error())
				break
			} else {
				if target != targetOld {
					changes.NotifySymLinkChanged()
					targetOld = target
				}
			}
		}
		<-time.After(1 * time.Second)
	}
}

func getSymlinkPath(path string) (bool, string) {
	dirs := strings.Split(path, "/")
	depth := len(dirs)

	for i := 0; i < depth; i++ {

		tpath := strings.Join(dirs[:depth-i], "/")

		fileInfo, err := os.Lstat(tpath)
		if err != nil {
			log.Println("Error: Unable to open filepath: ", tpath, err.Error())
			continue
		}

		// Return if tpath is a symlink
		if fileInfo.Mode()&os.ModeSymlink == os.ModeSymlink {
			return true, tpath
		}
	}

	return false, path
}
