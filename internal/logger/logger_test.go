package logger

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestSetLevel(t *testing.T) {
	SetLevel("debug")
	SetLevel("info")
	SetLevel("warn")
	SetLevel("error")
	// Should not panic with invalid level
	SetLevel("invalid")
}

func TestLevels(t *testing.T) {
	// Capture stderr
	originalStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	SetLevel("debug")
	Debug("test debug %s", "message")
	Info("test info")
	Warn("test warn")
	Error("test error")

	w.Close()
	os.Stderr = originalStderr

	var output bytes.Buffer
	io.Copy(&output, r)
	result := output.String()

	if !strings.Contains(result, "DEBUG") {
		t.Error("expected DEBUG in output")
	}
	if !strings.Contains(result, "INFO") {
		t.Error("expected INFO in output")
	}
	if !strings.Contains(result, "WARN") {
		t.Error("expected WARN in output")
	}
	if !strings.Contains(result, "ERROR") {
		t.Error("expected ERROR in output")
	}
}

func TestLevelFiltering(t *testing.T) {
	SetLevel("error")

	var buf bytes.Buffer
	originalStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	Debug("should not appear")
	Info("should not appear")
	Warn("should not appear")
	Error("should appear")

	w.Close()
	os.Stderr = originalStderr

	io.Copy(&buf, r)
	result := buf.String()

	if strings.Contains(result, "should not appear") {
		t.Error("lower level messages should not appear")
	}
	if !strings.Contains(result, "should appear") {
		t.Error("error message should appear")
	}
}
