//go:build darwin

package apply

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestSetCaptureTimesBirthtime(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "x.jpg")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	want := time.Date(2019, 3, 14, 14, 43, 55, 0, time.UTC).Unix()
	if err := setCaptureTimes(path, want); err != nil {
		t.Fatal(err)
	}
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		t.Fatal(err)
	}
	if st.Mtim.Sec != want {
		t.Fatalf("mtime = %d, want %d", st.Mtim.Sec, want)
	}
	if st.Btim.Sec != want {
		t.Fatalf("birthtime = %d, want %d", st.Btim.Sec, want)
	}
}
