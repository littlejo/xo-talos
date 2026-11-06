package main

import (
	"errors"
	"testing"
)

func TestCleanError(t *testing.T) {
	err := errors.New("get SR 3465240e: failed to do request Get \"http://127.0.0.1:1/rest/v0/srs/3465240e\": dial tcp 127.0.0.1:1: connect: connection refused%!(EXTRA string=http://127.0.0.1:1/rest/v0/srs/3465240e)")
	want := "get SR 3465240e: failed to do request Get \"http://127.0.0.1:1/rest/v0/srs/3465240e\": dial tcp 127.0.0.1:1: connect: connection refused"
	if got := cleanError(err); got != want {
		t.Errorf("cleanError = %q", got)
	}

	err = errors.New("create VM: API error: 404 Not Found - {\n  \"error\": \"no such VM-template x\",\n  \"data\": {\n    \"id\": \"x\"\n  }\n}")
	want = `create VM: API error: 404 Not Found - { "error": "no such VM-template x", "data": { "id": "x" } }`
	if got := cleanError(err); got != want {
		t.Errorf("cleanError = %q", got)
	}
}
