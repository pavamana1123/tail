package tail

import (
	"bufio"
	_ "fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"
	// "github.com/hpcloud/tail/ratelimiter"
)

var defaultConfig Config = Config{
	BufferSize:  100,
	MaxLineSize: 1048576,
	Follow:      true,
	ReOpen:      true,
	Poll:        false,
	MustExist:   true,
	PosFile:     "",
	Location:    &SeekInfo{Offset: 0, Whence: 0}}

func TestPSymlinkChange(t *testing.T) {

	testName := "symtest"
	testFile1 := "symtest1.log"
	testFile2 := "symtest2.log"
	testLink := "symtest"
	tailTest := NewTailTest(testName, t)

	tailTest.CreateFile(testFile1, "")
	tailTest.CreateFile(testFile2, "")
	tailTest.CreateLink(testFile1, testLink)

	tHandler := tailTest.StartTail(testLink, defaultConfig)

	defer tailTest.Cleanup(tHandler, true)

	tailTest.AppendFile(testLink, "line1\n")
	log.Println("Waiting for line1")
	testLine := <-tHandler.Lines

	if testLine.Text != "line1" {
		t.Error("line1 not recieved")
		t.Fail()
	} else {
		log.Println("Line1 recieved")
	}

	tailTest.Relink(testFile2, testLink)
	then := time.Now()
	tailTest.AppendFile(testLink, "line2\n")
	tailTest.AppendFile(testLink, "line3\n")

	log.Println("Symlink changed...waiting to detect")
	log.Println("Waiting for line2")
	testLine = <-tHandler.Lines
	if testLine.Text != "line2" {
		t.Error("line2 not recieved")
		t.Fail()
	} else {
		log.Println("Line2 recieved after", time.Since(then))
	}
}

func TestXNoLogLossAfterSymLinkChange(t *testing.T) {
	testName := "symtest_no_log_loss_with_logging"
	testFile1 := "symtest_no_log_loss_with_logging1.log"
	testFile2 := "symtest_no_log_loss_with_logging2.log"
	testLink := "symtest_no_log_loss_with_logging"

	tailTest := NewTailTest(testName, t)
	tailTest.CreateFile(testFile1, "")
	tailTest.CreateFile(testFile2, "")
	tailTest.CreateLink(testFile1, testLink)

	tHandler := tailTest.StartTail(testLink, defaultConfig)
	defer tailTest.Cleanup(tHandler, true)

	tailTest.AppendFile(testLink, "line1\n")
	log.Println("Waiting for line1")

	testLine := <-tHandler.Lines

	if testLine.Text != "line1" {
		t.Error("line1 not recieved")
		t.Fail()
	} else {
		log.Println("Line1 recieved")
	}

	totalLines := 0
	done := make(chan struct{})

	go func() {
		for i := 0; i < 1000; i++ {
			testLine = <-tHandler.Lines
			if testLine.Text != strconv.Itoa(i) {
				t.Error("line", i, "not recieved")
				t.Fail()
			}
			totalLines++
		}
		done <- struct{}{}
	}()

	then := time.Now()

	go func() {
		for i := 0; i < 1000; i++ {
			tailTest.AppendFile(testLink, strconv.Itoa(i)+"\n")
			<-time.After(10 * time.Millisecond)
		}
	}()

	<-time.After(5 * time.Second)

	tailTest.Relink(testFile2, testLink)
	log.Println("Symlink changed after", time.Since(then), "of logging with line recieved", totalLines)
	<-done

	log.Println("All lines recieved after", time.Since(then))
}

func TestPNoLogLossAfterBufferOverflow(t *testing.T) {
	testName := "symtest_no_log_loss_with_buffer_ovf"
	testFile := "symtest_no_log_loss_with_buffer_ovf.log"

	fourMillionBytes := make([]byte, 4000000)

	for i := 0; i < 4000000-2; i += 2 {
		fourMillionBytes[i] = 65
		fourMillionBytes[i+1] = 10
	}

	fourMillionBytes[4000000-2] = 66
	fourMillionBytes[4000000-1] = 10

	twoMillionLines := string(fourMillionBytes)

	tailTest := NewTailTest(testName, t)
	tailTest.CreateFile(testFile, "")

	tHandler := tailTest.StartTail(testFile, defaultConfig)
	defer tailTest.Cleanup(tHandler, true)

	tailTest.AppendFile(testFile, twoMillionLines)

	for i := 0; i < 2000000-1; i++ {
		testLine := <-tHandler.Lines
		if testLine.Text != "A" {
			t.Error("Line received was not A")
			t.Fail()
		}
	}

	testLine := <-tHandler.Lines
	if testLine.Text != "B" {
		t.Error("Last line was not B")
		t.Fail()
	}

	log.Println("All lines recieved")
}

func TestPPosFileUpdate(t *testing.T) {
	testName := "pos_file_update"
	testFile := "pos_file_update"

	config := Config{
		BufferSize:  100,
		MaxLineSize: 1048576,
		Follow:      true,
		ReOpen:      true,
		Poll:        false,
		MustExist:   true,
		PosFile:     "/tmp/" + testFile + "_lastSeekPosition",
		Location:    &SeekInfo{Offset: 0, Whence: 0},
	}

	posF, err := os.Open(config.PosFile)
	if err != nil {
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}

	posF.WriteString("")
	posF.Close()

	tailTest := NewTailTest(testName, t)
	tailTest.CreateFile(testFile, "8 bytes\n")

	tHandler := tailTest.StartTail(testFile, config)
	<-time.After(1 * time.Millisecond)
	tHandler.Stop()

	if s := tailTest.ReadPosFile(config.PosFile); s != "8" {
		log.Println(s)
		t.Fail()
	}
}

func TestPMaxLinePartition(t *testing.T) {
	testName := "max_line_partition"
	testFile := "max_line_partition.log"

	fiftyMB := defaultConfig.MaxLineSize * 50

	fiftyMBBytes := make([]byte, fiftyMB)

	log.Println("starting byte array ")

	for i := 0; i < fiftyMB; i++ {
		fiftyMBBytes[i] = 65
	}

	log.Println("byte created")

	fiftyMBLine := string(fiftyMBBytes)

	tailTest := NewTailTest(testName, t)
	tailTest.CreateFile(testFile, fiftyMBLine)

	log.Println("file created")

	tHandler := tailTest.StartTail(testFile, defaultConfig)
	defer tailTest.Cleanup(tHandler, true)

	for i := 0; i < 50; i++ {
		testLine := <-tHandler.Lines
		log.Println("reding lines")
		if testLine.Text[0] != 65 || testLine.Text[len(testLine.Text)] != 65 {
			t.Error("Line received was not A")
			t.Fail()
		}
	}

	log.Println("All lines recieved")
}

func TestPReadSlice(t *testing.T) {
	testName := "read_slice"
	testFile := "read_slice.log"

	longLine := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

	tailTest := NewTailTest(testName, t)
	tailTest.CreateFile(testFile, longLine)

	file, err := os.Open(tailTest.path + "/" + testFile)
	if err != nil {
		t.Error(err)
		return
	}

	reader := bufio.NewReaderSize(file, 1400)
	slice, err := reader.ReadSlice(10)
	if err != nil {
		if !(err == bufio.ErrBufferFull || err.Error() == "EOF") {
			t.Error(err)
		}
	}
	log.Println(len(longLine), len(slice), string(slice))

}

func TestImpulse(t *testing.T) {
	testName := "impulse"
	testFile := "impulse.log"

	tailTest := NewTailTest(testName, t)
	tailTest.CreateFile(testFile, "")

	var impulseConfig Config = Config{
		BufferSize:  1000000,
		MaxLineSize: 1048576,
		Follow:      true,
		ReOpen:      true,
		Poll:        false,
		MustExist:   true,
		PosFile:     "",
		Location:    &SeekInfo{Offset: 0, Whence: 0}}

	tHandler := tailTest.StartTail(testFile, impulseConfig)

	length := uint64(1024)

	lineBytes := make([]byte, length)

	for i := uint64(0); i < length-1; i++ {
		lineBytes[i] = 65
	}
	lineBytes[len(lineBytes)-1] = 10

	linesBytes := lineBytes
	for i := 0; i < impulseConfig.BufferSize; i++ {
		linesBytes = append(linesBytes, lineBytes...)
	}

	tailTest.AppendFile(testFile, string(linesBytes))

	for i := 0; i < impulseConfig.BufferSize; i++ {
		log.Println(<-tHandler.Lines)
	}

}

func (t TailTest) CreateLink(name string, link string) {
	_, err := exec.Command("ln", "-sf", name, t.path+"/"+link).Output()
	if err != nil {
		t.Fatal(err)
	}
}

func (t TailTest) Relink(name string, link string) {
	_, err := exec.Command("ln", "-sf", name, t.path+"/"+link).Output()
	if err != nil {
		t.Fatal(err)
	}
}

func (t TailTest) ReadPosFile(posFile string) string {
	f, err := os.Open(posFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	seekPos := make([]byte, 10)
	_, err = f.Read(seekPos)
	if err != nil {
		t.Fatal(err)
	}

	return string(seekPos[0:1])
}
