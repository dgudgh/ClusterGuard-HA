//go:build !linux

package main

import (
	"errors"
	"net"
)

func peerUID(*net.UnixConn) (uint32, error) {
	return 0, errors.New("software update helper peer credentials require Linux")
}
