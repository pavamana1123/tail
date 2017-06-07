// Copyright (c) 2015 HPE Software Inc. All rights reserved.
// Copyright (c) 2013 ActiveState Software Inc. All rights reserved.

package watch

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"strings"

	"github.com/pavamana1123/tail/util"
	"gopkg.in/fsnotify.v1"
	"gopkg.in/tomb.v1"
)

// InotifyFileWatcher uses inotify to monitor file changes.
type InotifyFileWatcher struct {
	Filename string
	Size     int64
}

var (
	wg         sync.WaitGroup
	notSymLink error
)

func init() {
	notSymLink = errors.New("Not a symlink")
}

func NewInotifyFileWatcher(filename string) *InotifyFileWatcher {
	fw := &InotifyFileWatcher{filepath.Clean(filename), 0}
	return fw
}

func (fw *InotifyFileWatcher) BlockUntilExists(t *tomb.Tomb) error {
	err := WatchCreate(fw.Filename)
	if err != nil {
		log.Println("WatchCreate", err)
		return err
	}

	defer RemoveWatchCreate(fw.Filename)

	// Do a real check now as the file might have been created before
	// calling `WatchFlags` above.
	if _, err = os.Stat(fw.Filename); err != nil && !os.IsNotExist(err) {
		// file exists, or stat returned an error.
		log.Println("File exists, or stat returned an error.", err)
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

	go changes.detectInotifyChanges(t, fw)
	return changes, nil
}

func (changes *FileChanges) detectInotifyChanges(t *tomb.Tomb, fw *InotifyFileWatcher) {

	var (
		evt       fsnotify.Event
		symCh, ok bool
	)

	defer func() {
		RemoveWatch(fw.Filename)
		if symCh {
			changes.NotifySymLinkChanged()
		}
	}()

	events := Events(fw.Filename)

	symlinkChange, err := changes.detectSymlinkChanges(t, fw)
	if err != nil {
		// error occurs only if path is not a symlink
		if err == notSymLink {
			log.Println(err.Error(), ":", fw.Filename)
		}
	}

	for {
		prevSize := fw.Size

		select {
		case evt, ok = <-events:
			if !ok {
				return
			}
			break
		case <-symlinkChange:
			symCh = true
			return
		case <-t.Dying():
			return
		}

		switch {
		case evt.Op&fsnotify.Remove == fsnotify.Remove:
			log.Println("File", fw.Filename, "deleted")
			fallthrough

		// // it was seen that fsnotify package is returning Chmod event for file delete
		// case evt.Op&fsnotify.Chmod == fsnotify.Chmod:
		// 	fallthrough

		case evt.Op&fsnotify.Rename == fsnotify.Rename:
			log.Println("File ", fw.Filename, "moved/renamed")
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

func (changes *FileChanges) detectSymlinkChanges(t *tomb.Tomb, fw *InotifyFileWatcher) (chan struct{}, error) {

	symLinkChanged := make(chan struct{})

	isSymLink, symlinkPath := getSymlinkPath(fw.Filename)

	if !isSymLink {
		return symLinkChanged, notSymLink
	}

retry:
	target, err := getInode(symlinkPath)
	if err != nil {
		NewInotifyFileWatcher(symlinkPath).BlockUntilExists(t)
		goto retry
	}

	go changes.pollSymlinkForChange(t, symLinkChanged, symlinkPath, target)

	return symLinkChanged, nil

}

func (changes *FileChanges) pollSymlinkForChange(t *tomb.Tomb, symLinkChanged chan struct{}, symlinkPath string, targetOld uint64) {

	var (
		target uint64
		err    error
	)

	for {
		select {
		case <-t.Dying():
			return
		default:
			target, err = getInode(symlinkPath)
			if err != nil {
				NewInotifyFileWatcher(symlinkPath).BlockUntilExists(t)
				continue
			}
			if target != targetOld {
				symLinkChanged <- struct{}{}
				return
			}

		}
		<-time.After(1 * time.Second)
	}
}

func getSymlinkPath(path string) (bool, string) {
	dirs := strings.Split(path, "/")
	depth := len(dirs)

	for i := 0; i < depth-1; i++ {

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

func getInode(path string) (uint64, error) {
	var stat syscall.Stat_t
	if err := syscall.Stat(path, &stat); err != nil {
		return 0, err
	}
	return stat.Ino, nil
}
