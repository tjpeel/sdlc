//go:build windows

package workrun

import "fmt"

func privatePublisherParent(path string) error {
	return fmt.Errorf("publisher parent ACL validation is not yet supported on Windows")
}
