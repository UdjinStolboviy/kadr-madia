package sdnotify

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestNotifySocket(t *testing.T) {
	if ok, err := NotifySocket("", Ready); ok || err != nil {
		t.Fatalf("empty socket: %v %v", ok, err)
	}
	// Unix socket paths are limited to ~104 bytes; t.TempDir can be longer on macOS.
	dir, err := os.MkdirTemp("", "sdn")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "n.sock")
	l, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ok, err := NotifySocket(path, Ready)
	if !ok || err != nil {
		t.Fatalf("notify: %v %v", ok, err)
	}
	buf := make([]byte, 64)
	n, _, err := l.ReadFromUnix(buf)
	if err != nil || string(buf[:n]) != Ready {
		t.Fatalf("got %q %v", buf[:n], err)
	}
}
