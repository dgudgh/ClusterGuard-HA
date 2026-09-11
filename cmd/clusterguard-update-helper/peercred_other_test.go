//go:build darwin

package main

import (
	"os"
	"testing"
)

func TestCredentialListenerFailsClosedWithoutKernelCredentialSupport(t *testing.T) {
	assertCredentialRejected(t, uint32(os.Getuid()))
}
