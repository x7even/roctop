package main

import (
	"fmt"
	"sync"
	"time"
)

const maxLogEntries = 200

type logEntry struct {
	ts  time.Time
	msg string
}

var (
	logEntries []logEntry
	logMu      sync.Mutex
)

func logf(format string, args ...interface{}) {
	logMu.Lock()
	defer logMu.Unlock()
	logEntries = append(logEntries, logEntry{
		ts:  time.Now(),
		msg: fmt.Sprintf(format, args...),
	})
	if len(logEntries) > maxLogEntries {
		logEntries = logEntries[len(logEntries)-maxLogEntries:]
	}
}

func getLogEntries() []logEntry {
	logMu.Lock()
	defer logMu.Unlock()
	cp := make([]logEntry, len(logEntries))
	copy(cp, logEntries)
	return cp
}
