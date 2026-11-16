// Package sdnotify implements the systemd sd_notify protocol (Type=notify)
// without external dependencies.
package sdnotify

import (
	"fmt"
	"net"
	"os"
	"strings"
)

// Well-known states.
const (
	Ready    = "READY=1"
	Stopping = "STOPPING=1"
)

// Notify sends state to the socket in $NOTIFY_SOCKET. It returns false
// (and no error) when not running under systemd.
func Notify(state string) (bool, error) {
	return NotifySocket(os.Getenv("NOTIFY_SOCKET"), state)
}

// NotifySocket sends state to the given datagram socket path. A leading '@'
// denotes a Linux abstract socket.
func NotifySocket(socket, state string) (bool, error) {
	if socket == "" {
		return false, nil
	}
	name := socket
	if strings.HasPrefix(name, "@") {
		name = "\x00" + name[1:]
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: name, Net: "unixgram"})
	if err != nil {
		return false, fmt.Errorf("sdnotify: dial: %w", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(state)); err != nil {
		return false, fmt.Errorf("sdnotify: write: %w", err)
	}
	return true, nil
}
