package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWipeBytes(t *testing.T) {
	b := []byte{1, 2, 3, 4, 5}
	WipeBytes(b)
	for i, v := range b {
		if v != 0 {
			t.Errorf("b[%d] = %d, want 0", i, v)
		}
	}
}

func TestWipeBytesEmpty(t *testing.T) {
	require.NotPanics(t, func() { WipeBytes(nil) })
	require.NotPanics(t, func() { WipeBytes([]byte{}) })
}
