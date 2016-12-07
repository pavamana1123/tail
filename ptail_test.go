package tail

import (
	_ "fmt"
	"log"
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

func TestSymlinkChange(t *testing.T) {

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

func TestNoLogLossAfterSymLinkChangeAndBufferOverflow(t *testing.T) {
	testName := "symtest_no_log_loss"
	testFile1 := "symtest_no_log_loss1.log"
	testFile2 := "symtest_no_log_loss2.log"
	testLink := "symtest_no_log_loss"

	fourMillionBytes := make([]byte, 4000000)

	for i := 0; i < 4000000-2; i += 2 {
		fourMillionBytes[i] = 65
		fourMillionBytes[i+1] = 10
	}

	fourMillionBytes[4000000-2] = 66
	fourMillionBytes[4000000-1] = 10

	twoMillionLines := string(fourMillionBytes)

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
	tailTest.AppendFile(testLink, twoMillionLines)

	log.Println("Symlink changed...waiting to detect")
	log.Println("Waiting for line2")

	for i := 0; i < 2000000-1; i++ {
		testLine = <-tHandler.Lines
		if testLine.Text != "A" {
			t.Error("Line received was not A")
			t.Fail()
		}
	}

	testLine = <-tHandler.Lines
	if testLine.Text != "B" {
		t.Error("Last line was not B")
		t.Fail()
	}

	log.Println("All lines recieved after", time.Since(then))
}

func TestNoLogLossAfterSymLinkChangeAndLogging(t *testing.T) {
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
			<-time.After(100 * time.Millisecond)
		}
	}()

	<-time.After(50 * time.Second)

	tailTest.Relink(testFile2, testLink)
	log.Println("Symlink changed after", time.Since(then), "of logging with line recieved", totalLines)
	<-done

	log.Println("All lines recieved after", time.Since(then))
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
