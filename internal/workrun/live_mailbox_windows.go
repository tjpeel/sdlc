//go:build windows

package workrun

import (
	"fmt"
	"os"
)

const liveDiagnosticsSupported = false

func openDiagnosticMailbox(string) (*os.File, error) {
	return nil, fmt.Errorf("live diagnostic mailbox integrity checks unavailable on this host")
}
