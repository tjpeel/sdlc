//go:build windows

package controllerasset

import "fmt"

func privateImmutable(path string, directory bool) error {
	return fmt.Errorf("trusted publisher assets require Unix ownership checks")
}
