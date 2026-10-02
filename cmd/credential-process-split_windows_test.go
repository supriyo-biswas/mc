//go:build windows

package cmd

import (
	"reflect"
	"testing"
)

func TestSplitCredentialProcess(t *testing.T) {
	args, err := splitCredentialProcess(`"C:\Program Files\credential-helper.exe" --profile "production account"`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{`C:\Program Files\credential-helper.exe`, "--profile", "production account"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("expected %#v, got %#v", want, args)
	}
}
