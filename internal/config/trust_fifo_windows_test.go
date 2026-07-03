//go:build windows

package config

import "fmt"

func makeTrustTestFIFO(path string) error {
	return fmt.Errorf("FIFO unsupported on Windows: %s", path)
}
