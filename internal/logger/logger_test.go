package logger

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// TestSetLevelInvalidKeepsCurrent verifies that unknown (or empty) levels are
// ignored instead of silently dropping to the zero value (debug) — the old
// behavior turned typos and missing log_level into full debug logging.
func TestSetLevelInvalidKeepsCurrent(t *testing.T) {
	SetLevel("info")
	if level != "info" {
		t.Fatalf("expected level info, got %q", level)
	}

	SetLevel("verobse")
	if level != "info" {
		t.Errorf("invalid level must be ignored, got %q", level)
	}

	SetLevel("")
	if level != "info" {
		t.Errorf("empty level must be ignored, got %q", level)
	}

	SetLevel("ERROR")
	if level != "error" {
		t.Errorf("valid level must be applied (case-insensitive), got %q", level)
	}
}

func TestLevels(t *testing.T) {
	// Capture stderr
	originalStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w

	SetLevel("debug")
	Debug("test debug %s", "message")
	Info("test info")
	Warn("test warn")
	Error("test error")

	w.Close()
	os.Stderr = originalStderr

	var output bytes.Buffer
	if _, err := io.Copy(&output, r); err != nil {
		t.Fatal(err)
	}
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
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w

	Debug("should not appear")
	Info("should not appear")
	Warn("should not appear")
	Error("should appear")

	w.Close()
	os.Stderr = originalStderr

	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	result := buf.String()

	if strings.Contains(result, "should not appear") {
		t.Error("lower level messages should not appear")
	}
	if !strings.Contains(result, "should appear") {
		t.Error("error message should appear")
	}
}
