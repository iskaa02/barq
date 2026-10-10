package runner

import (
	"os"
	"testing"

	"github.com/zalando/go-keyring"
)

// Tests must never touch the real keyring or the real ~/.barq.
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "barq-home-")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	os.Setenv("USERPROFILE", home)
	keyring.MockInit()
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}
