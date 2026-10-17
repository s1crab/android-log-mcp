//go:build windows

package service

import (
	"os"
	"testing"
	"time"
)

// A real read handle on wanted.json (Go opens without FILE_SHARE_DELETE, like
// the successor's read, an indexer or antivirus) makes the writer's rename
// fail; the writer keeps the content, retries and converges once the handle
// is closed (design 7.1 case 14, Windows-only part).
func TestWantedRenameWhileFileOpen(t *testing.T) {
	t.Parallel()
	_, withADB := installFake(t, "FAKEA", "FAKEB")
	ctrl := testControl(t)
	s := startTest(t, ctrl, "0.4.0", withADB, func(o *Options) {
		o.Wanted.Debounce = 5 * time.Millisecond
		o.Wanted.RetryBase, o.Wanted.RetryMax = 10*time.Millisecond, 40*time.Millisecond
	})
	s.Manager().Want("FAKEA", true)
	waitUntil(t, "the first write", 30*time.Second, func() bool {
		f, _ := readWanted(ctrl.WantedPath())
		return f != nil && len(f.Devices) == 1
	})
	h, err := os.Open(ctrl.WantedPath())
	if err != nil {
		t.Fatal(err)
	}
	s.Manager().Want("FAKEB", true)
	h.Close()
	waitUntil(t, "the content to converge once the handle is closed", 30*time.Second, func() bool {
		f, _ := readWanted(ctrl.WantedPath())
		return f != nil && len(f.Devices) == 2
	})
	if fileExists(ctrl.WantedPath() + ".tmp") {
		t.Fatal("temp file left behind")
	}
}
