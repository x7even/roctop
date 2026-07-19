package main

import (
	"os"
	"testing"
)

func TestProcNameSelf(t *testing.T) {
	name := procName(os.Getpid())
	if name == "" || name == "unknown" {
		t.Fatalf("procName(self) = %q, want a real process name", name)
	}
}
