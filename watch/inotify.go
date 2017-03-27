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

	defer func() {
		RemoveWatchCreate(fw.Filename)
		defer log.Println("Create watch removed inside BlockUntilExists")
	}()

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
	log.Println("Setting fw.Size:", pos)

	// var once sync.Once
	// common tomb for detectSymlinkChanges and detectInotifyChanges go routines
	// ct := new(tomb.Tomb)

	// polling for changes in symlink target, if symlink exists
	// if path does not contain symlinks, the go routine stops
	// detecting file events other than symlink change.
	go changes.detectInotifyChanges(t, fw)
	return changes, nil
}

func (changes *FileChanges) detectInotifyChanges(t *tomb.Tomb, fw *InotifyFileWatcher) {

	log.Println("@detectInotifyChanges")

	defer func() {
		RemoveWatch(fw.Filename)
		if symCh {
			changes.NotifySymLinkChanged()
		}
		log.Println("Exiting detectInotifyChanges")
	}()

	events := Events(fw.Filename)

	symlinkChange, err := changes.detectSymlinkChanges(t, fw)
	if err != nil {
		// error occurs only if path is not a symlink
		log.Println(err)
	}

	var (
		evt   fsnotify.Event
		symCh bool
		ok    bool
	)

	for {
		prevSize := fw.Size

		select {
		case evt, ok = <-events:
			log.Println("@case evt, ok = <-events:", evt, ok)
			if !ok {
				return
			}
			break
		case <-symlinkChange:
			log.Println("Symlink changed")
			symCh = true
			// changes.NotifySymLinkChanged()
			return
		case <-t.Dying():
			log.Println("@case <-t.Dying():@detectInotifyChanges")
			return
			// case <-ct.Dead():
			// 	log.Println("@case <-ct.Dead():@detectInotifyChanges")
			// 	return
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
			log.Println("fsnotify.Write detected:fw.Size:", fw.Size)

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
		return symLinkChanged, errors.New("Not a symlink: " + fw.Filename)
	}

retry:
	target, err := getInode(symlinkPath)
	if err != nil {
		log.Println("getInode:", symlinkPath, err.Error())
		select {
		case <-time.After(1 * time.Second):
			goto retry
			// case <-t.Dying():
			// 	return symLinkChanged, err
			// case <-ct.Dead():
			// 	log.Println("@case <-ct.Dead():@getInode")
			// 	return
		}

	}

	go changes.pollSymlinkForChange(t, symLinkChanged, symlinkPath, target)

	return symLinkChanged, nil

}

func (changes *FileChanges) pollSymlinkForChange(t *tomb.Tomb, symLinkChanged chan struct{}, symlinkPath string, targetOld uint64) {

	defer log.Println("Exiting pollSymlinkForChange")

	log.Println("@pollSymlinkForChange")

	var (
		target uint64
		err    error
	)

	for {
		select {
		case <-t.Dying():
			return
		// case <-ct.Dead():
		// 	log.Println("@case <-ct.Dead():@pollSymlinkForChange")
		// 	return
		default:
			target, err = getInode(symlinkPath)
			if err != nil {
				log.Println("getInode:", err, symlinkPath)
				<-time.After(1 * time.Second)
				continue
			}
			if target != targetOld {
				symLinkChanged <- struct{}{}
				log.Println("@symLinkChanged <- struct{}{}")
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
