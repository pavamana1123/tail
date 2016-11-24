package watch

type FileChanges struct {
	Modified       chan bool // Channel to get notified of modifications
	Truncated      chan bool // Channel to get notified of truncations
	Deleted        chan bool // Channel to get notified of deletions/renames
	SymLinkChanged chan bool // Channel to get notified of symlink changes
}

func NewFileChanges() *FileChanges {
	return &FileChanges{
		make(chan bool), make(chan bool), make(chan bool), make(chan bool)}
}

func (fc *FileChanges) NotifyModified() {
	sendOnlyIfEmpty(fc.Modified)
}

func (fc *FileChanges) NotifyTruncated() {
	sendOnlyIfEmpty(fc.Truncated)
}

func (fc *FileChanges) NotifyDeleted() {
	sendOnlyIfEmpty(fc.Deleted)
}

func (fc *FileChanges) NotifySymLinkChanged() {
	sendOnlyIfEmpty(fc.SymLinkChanged)
}

// sendOnlyIfEmpty sends on a bool channel only if the channel has no
// backlog to be read by other goroutines. This concurrency pattern
// can be used to notify other goroutines if and only if they are
// looking for it (i.e., subsequent notifications can be compressed
// into one).
func sendOnlyIfEmpty(ch chan bool) {
	select {
	case ch <- true:
	default:
	}
}
