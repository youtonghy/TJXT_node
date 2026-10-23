package node

import (
	"os"
	"testing"
)

// Tests never touch the service's spool directory.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "tjxt-node-spool-")
	if err != nil {
		panic(err)
	}
	os.Setenv("TJXT_SPOOL_DIR", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}
