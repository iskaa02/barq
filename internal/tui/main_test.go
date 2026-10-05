package tui

import (
	"os"
	"testing"

	"github.com/zalando/go-keyring"
)

// Tests must never touch the real keyring.
func TestMain(m *testing.M) {
	keyring.MockInit()
	os.Exit(m.Run())
}
