package main

import "testing"

func TestCheckNvimVersion(t *testing.T) {
	for line, ok := range map[string]bool{
		"NVIM v0.10.0":          true,
		"NVIM v0.11.2-dev+g123": true,
		"NVIM v1.0.0":           true,
		"NVIM v0.9.5":           false,
		"something else":        false,
	} {
		if err := checkNvimVersion(line); (err == nil) != ok {
			t.Errorf("%q: err=%v, want ok=%v", line, err, ok)
		}
	}
}

func TestLooksLikeRequest(t *testing.T) {
	for arg, want := range map[string]bool{
		"https://x.test/a": true, "localhost:8080": true, "localhost": true,
		"127.0.0.1:3000/x": true, "api.test/users": true, "curl https://x": true,
		"rm": false, "frobnicate": false, "--nope": false, "x": false,
	} {
		if got := looksLikeRequest(arg); got != want {
			t.Errorf("%q: got %v, want %v", arg, got, want)
		}
	}
	if !removedCommands["rm"] || !removedCommands["rename"] || removedCommands["run"] {
		t.Error("removedCommands")
	}
}
