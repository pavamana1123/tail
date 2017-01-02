// Copyright (c) 2015 HPE Software Inc. All rights reserved.
// Copyright (c) 2013 ActiveState Software Inc. All rights reserved.

package tail

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/pavamana1123/tail/ratelimiter"
	"github.com/pavamana1123/tail/util"
	"github.com/pavamana1123/tail/watch"
	"gopkg.in/tomb.v1"
)

var (
	ErrStop = fmt.Errorf("tail should now stop")
)

type Line struct {
	Text []byte
	Time time.Time
	Err  error // Error from tail
}

// SeekInfo represents arguments to `os.Seek`
type SeekInfo struct {
	Offset int64
	Whence int // os.SEEK_*
}

type logger interface {
	Fatal(v ...interface{})
	Fatalf(format string, v ...interface{})
	Fatalln(v ...interface{})
	Panic(v ...interface{})
	Panicf(format string, v ...interface{})
	Panicln(v ...interface{})
	Print(v ...interface{})
	Printf(format string, v ...interface{})
	Println(v ...interface{})
}

// Config is used to specify how a file must be tailed.
type Config struct {
	// File-specifc
	Location    *SeekInfo // Seek to this location before tailing
	ReOpen      bool      // Reopen recreated files (tail -F)
	MustExist   bool      // Fail early if the file does not exist
	Poll        bool      // Poll for file changes instead of using inotify
	Pipe        bool      // Is a named pipe (mkfifo)
	RateLimiter *ratelimiter.LeakyBucket

	// Generic IO
	Follow      bool // Continue looking for new lines (tail -f)
	MaxLineSize int  // If non-zero, split longer lines into multiple lines

	PosFile string
	// Logger, when nil, is set to tail.DefaultLogger
	// To disable logging: set field to tail.DiscardingLogger
	Logger logger
}

type Tail struct {
	Filename string
	Lines    chan *Line
	Config

	file   *os.File
	reader *bufio.Reader

	watcher watch.FileWatcher
	changes *watch.FileChanges

	tomb.Tomb // provides: Done, Kill, Dying

	lk sync.Mutex
}

var (
	// DefaultLogger is used when Config.Logger == nil
	DefaultLogger = log.New(os.Stderr, "", log.LstdFlags)
	// DiscardingLogger can be used to disable logging output
	DiscardingLogger = log.New(ioutil.Discard, "", 0)
)

// TailFile begins tailing the file. Output stream is made available
// via the `Tail.Lines` channel. To handle errors during tailing,
// invoke the `Wait` or `Err` method after finishing reading from the
// `Lines` channel.
func TailFile(filename string, config Config) (*Tail, error) {
	if config.ReOpen && !config.Follow {
		util.Fatal("cannot set ReOpen without Follow.")
	}

	t := &Tail{
		Filename: filename,
		Lines:    make(chan *Line),
		Config:   config,
	}

	// when Logger was not specified in config, use default logger
	if t.Logger == nil {
		t.Logger = log.New(os.Stderr, "", log.LstdFlags)
	}

	if t.Poll {
		t.watcher = watch.NewPollingFileWatcher(filename)
	} else {
		t.watcher = watch.NewInotifyFileWatcher(filename)
	}

	if t.MustExist {
		var err error
		t.file, err = OpenFile(t.Filename)
		if err != nil {
			return nil, err
		}
	}

	go t.tailFileSync()

	return t, nil
}

// Return the file's current position, like stdio's ftell().
// But this value is not very accurate.
// it may readed one line in the chan(tail.Lines),
// so it may lost one line.
func (tail *Tail) Tell() (offset int64, err error) {
	if tail.file == nil {
		return
	}
	offset, err = tail.file.Seek(0, os.SEEK_CUR)
	if err != nil {
		return
	}

	tail.lk.Lock()
	defer tail.lk.Unlock()
	if tail.reader == nil {
		return
	}

	offset -= int64(tail.reader.Buffered())
	return
}

// Stop stops the tailing activity.
func (tail *Tail) Stop() error {
	tail.Kill(nil)
	return tail.Wait()
}

// StopAtEOF stops tailing as soon as the end of the file is reached.
func (tail *Tail) StopAtEOF() error {
	tail.Kill(errStopAtEOF)
	return tail.Wait()
}

var errStopAtEOF = errors.New("tail: stop at eof")

func (tail *Tail) close() {
	if tail.Config.PosFile != "" {

		newPos, err := tail.Tell()
		if err != nil {
			log.Println("TailReader: Unable to get position, not updating. ", err)
			return
		}

		err = ioutil.WriteFile(tail.Config.PosFile, []byte(strconv.FormatInt(newPos, 10)), 0644)
		if err != nil {
			log.Println("Failed to update position", newPos, err)
			return
		}

	}

	close(tail.Lines)
	tail.closeFile()
	tail.Done()
}

func (tail *Tail) closeFile() {
	if tail.file != nil {
		tail.file.Close()
		tail.file = nil
	}
}

func (tail *Tail) reopen() error {
	tail.closeFile()
	for {
		var err error
		tail.file, err = OpenFile(tail.Filename)
		if err != nil {
			if os.IsNotExist(err) {
				tail.Logger.Printf("Waiting for %s to appear...", tail.Filename)
				if err := tail.watcher.BlockUntilExists(&tail.Tomb); err != nil {
					if err == tomb.ErrDying {
						return err
					}
					return fmt.Errorf("Failed to detect creation of %s: %s", tail.Filename, err)
				}
				continue
			}
			return fmt.Errorf("Unable to open file %s: %s", tail.Filename, err)
		}
		break
	}
	return nil
}

func (tail *Tail) reopenTarget() error {
	tail.renewWatcher()
	return tail.reopen()
}

func (tail *Tail) renewWatcher() {
	tail.watcher = watch.NewInotifyFileWatcher(tail.Filename)
	tail.changes = nil
}

func (tail *Tail) readLine() ([]byte, error) {

	tail.lk.Lock()
	lineBytes, err := tail.reader.ReadSlice(10) //10 - byte value for \n
	tail.lk.Unlock()
	if err != nil {
		// Note ReadString "returns the data read before the error" in
		// case of an error, including EOF, so we return it as is. The
		// caller is expected to process it if err is EOF.
		return lineBytes, err
	}

	return lineBytes[:len(lineBytes)-1], err // removing the last byte - \n
}

func (tail *Tail) tailFileSync() {
	defer tail.close()

	if !tail.MustExist {
		// deferred first open.
		err := tail.reopen()
		if err != nil {
			if err != tomb.ErrDying {
				tail.Kill(err)
			}
			return
		}
	}

	// Seek to requested location on first open of the file.
	if tail.Location != nil {
		_, err := tail.file.Seek(tail.Location.Offset, tail.Location.Whence)
		// tail.Logger.Printf("Seeked %s - %+v\n", tail.Filename, tail.Location)
		if err != nil {
			tail.Killf("Seek error on %s: %s", tail.Filename, err)
			return
		}
	}

	tail.openReader()

	var offset int64 = 0
	var err error
	var line []byte

	// Read line by line.
	for {
		// do not seek in named pipes
		if !tail.Pipe {
			// grab the position in case we need to back up in the event of a half-line
			offset, err = tail.Tell()
			if err != nil {
				log.Println("Tell:", err.Error())

				tail.Kill(err)
				return
			}
		}

		line, err = tail.readLine()

		// Process `line` even if err is EOF.
		if err == nil || err == bufio.ErrBufferFull {
			tail.sendLine(line)
		} else if err == io.EOF {
			if !tail.Follow {
				if len(line) != 0 {
					tail.sendLine(line)
				}
				return
			}

			if tail.Follow && len(line) != 0 {
				// this has the potential to never return the last line if
				// it's not followed by a newline; seems a fair trade here
				err := tail.seekTo(SeekInfo{Offset: offset, Whence: 0})
				if err != nil {
					tail.Kill(err)

					return
				}
			}

			// When EOF is reached, wait for more data to become
			// available. Wait strategy is based on the `tail.watcher`
			// implementation (inotify or polling).
			err := tail.waitForChanges()
			if err != nil {
				if err != ErrStop {
					tail.Kill(err)
				}
				return
			}
		} else {
			// non-EOF error
			tail.Killf("Error reading %s: %s", tail.Filename, err)
			return
		}

		select {
		case <-tail.Dying():
			if tail.Err() == errStopAtEOF {
				continue
			}
			return
		default:
		}
	}
}

// waitForChanges waits until the file has been appended, deleted,
// moved or truncated. When moved or deleted - the file will be
// reopened if ReOpen is true. Truncated files are always reopened.
func (tail *Tail) waitForChanges() error {

	if tail.changes == nil {
		pos, err := tail.file.Seek(0, os.SEEK_CUR)
		if err != nil {
			log.Println("tail.file.Seek:", err.Error())
			return err
		}
		tail.changes, err = tail.watcher.ChangeEvents(&tail.Tomb, pos)
		if err != nil {
			log.Println("tail.watcher.ChangeEvents:", err.Error())
			return err
		}
	}

	select {
	case <-tail.changes.Modified:
		return nil
	case <-tail.changes.Deleted:
		if tail.ReOpen {
			// XXX: we must not log from a library.
			tail.Logger.Printf("Re-opening moved/deleted file %s ...", tail.Filename)
			if err := tail.reopen(); err != nil {
				log.Println("tail.ReOpen:", err.Error())
				return err
			}
			tail.Logger.Printf("Successfully reopened %s", tail.Filename)
			tail.openReader()
			tail.changes = nil
			return nil
		} else {
			tail.Logger.Printf("Stopping tail as file no longer exists: %s", tail.Filename)
			return ErrStop
		}
	case <-tail.changes.SymLinkChanged:
		// Always reopen files if symlink target is changed (Follow is true)
		tail.Logger.Printf("Re-opening new symlink target file %s ...", tail.Filename)
		if err := tail.reopenTarget(); err != nil {
			return err
		}
		tail.Logger.Printf("Successfully opened %s", tail.Filename)
		tail.openReader()
		return nil
	case <-tail.changes.Truncated:
		// Always reopen files if truncated (Follow is true)
		tail.Logger.Printf("Re-opening truncated file %s ...", tail.Filename)
		if err := tail.reopen(); err != nil {
			return err
		}
		tail.Logger.Printf("Successfully reopened truncated %s", tail.Filename)
		tail.openReader()
		return nil
	case <-tail.Dying():
		return ErrStop
	}
	panic("unreachable")
}

func (tail *Tail) openReader() {
	if tail.MaxLineSize > 0 {
		// add 2 to account for newline characters
		tail.reader = bufio.NewReaderSize(tail.file, tail.MaxLineSize+2)
	} else {
		tail.reader = bufio.NewReader(tail.file)
	}
}

func (tail *Tail) seekEnd() error {
	return tail.seekTo(SeekInfo{Offset: 0, Whence: os.SEEK_END})
}

func (tail *Tail) seekTo(pos SeekInfo) error {
	_, err := tail.file.Seek(pos.Offset, pos.Whence)
	if err != nil {
		return fmt.Errorf("Seek error on %s: %s", tail.Filename, err)
	}
	// Reset the read buffer whenever the file is re-seek'ed
	tail.reader.Reset(tail.file)
	return nil
}

// sendLine sends the line(s) to Lines channel, splitting longer lines
// if necessary. Return false if rate limit is reached.
func (tail *Tail) sendLine(line []byte) bool {

	tail.Lines <- &Line{line, time.Now(), nil}

	return true
}

// Cleanup removes inotify watches added by the tail package. This function is
// meant to be invoked from a process's exit handler. Linux kernel may not
// automatically remove inotify watches after the process exits.
func (tail *Tail) Cleanup() {
	watch.Cleanup(tail.Filename)
}
