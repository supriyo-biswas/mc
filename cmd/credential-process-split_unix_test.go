//go:build !windows

package cmd

import (
	"reflect"
	"testing"
)

func TestSplitCredentialProcess(t *testing.T) {
	args, err := splitCredentialProcess(`/opt/credential\ helper --profile "production account" 'literal value'`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/opt/credential helper", "--profile", "production account", "literal value"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("expected %#v, got %#v", want, args)
	}

	if _, err := splitCredentialProcess(`helper "unterminated`); err == nil {
		t.Fatal("expected malformed quoting to fail")
	}
}
