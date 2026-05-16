package logger

import (
	"fmt"
	"os"
	"strings"
	"sync"
)

var (
	mu     sync.Mutex
	level  = "info"
	levels = map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}
)

func SetLevel(l string) {
	mu.Lock()
	defer mu.Unlock()
	level = strings.ToLower(l)
}

func logf(lvl, format string, args ...interface{}) {
	mu.Lock()
	minLevel := levels[level]
	curLevel := levels[lvl]
	mu.Unlock()

	if curLevel < minLevel {
		return
	}

	prefix := fmt.Sprintf("[%s] ", strings.ToUpper(lvl))
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(os.Stderr, "%s%s\n", prefix, msg)
}

func Debug(format string, args ...interface{}) { logf("debug", format, args...) }
func Info(format string, args ...interface{})  { logf("info", format, args...) }
func Warn(format string, args ...interface{})  { logf("warn", format, args...) }
func Error(format string, args ...interface{}) { logf("error", format, args...) }
