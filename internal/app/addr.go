package app

import (
	"net"
	"strconv"
)

// portOfAddr extracts the port from a "host:port" listen address, or 0.
func portOfAddr(listen string) int {
	_, portText, err := net.SplitHostPort(listen)
	if err != nil {
		return 0
	}
	p, err := strconv.Atoi(portText)
	if err != nil {
		return 0
	}
	return p
}
