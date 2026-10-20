package main

import (
	"bytes"
	"testing"
)

func TestControllerRejectsUntrustedOrOversizedJob(t *testing.T) {
	for _, body := range []string{`{"Evidence":{"MalwareClean":true}}`, `{} {}`, `{"Input":"` + string(bytes.Repeat([]byte("A"), 1024)) + `"}`} {
		if _, err := readJob(bytes.NewBufferString(body), 512); err == nil {
			t.Fatal("untrusted controller request accepted")
		}
	}
}
