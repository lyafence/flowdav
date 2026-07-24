package config

// WipeBytes zeroes b to prevent casual key recovery.
func WipeBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
