package watch

import (
	"testing"

	tomb "gopkg.in/tomb.v1"
)

func TestBlockUntilExists(t *testing.T) {
	fw := NewInotifyFileWatcher("/Users/11126/a/b/c/file")

	if err := fw.BlockUntilExists(new(tomb.Tomb)); err != nil {
		t.Fail()
	}
}
