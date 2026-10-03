package instructions

import "os"

func openRegular(path string) (*os.File, error) {
	// The caller checks file identity and type before reading the opened handle.
	return os.Open(path)
}
