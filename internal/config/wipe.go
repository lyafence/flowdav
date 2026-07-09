package config

// WipeBytes overwrites b with zeros.
//
// Note: Go does not guarantee that this will not be optimized away by a
// future compiler. For now it matches the existing bridge.go pattern and
// prevents casual memory inspection from recovering key material after
// shutdown.
func WipeBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
