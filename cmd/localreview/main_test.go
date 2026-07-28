package main

import "testing"

func TestAuthAndRemoteCommandUsageIsValidatedBeforeDaemonAccess(t *testing.T) {
	for _, command := range [][]string{
		nil,
		{"logout"},
		{"login", "unexpected"},
	} {
		if err := authCommand(command); err == nil {
			t.Fatalf("auth command %v unexpectedly succeeded", command)
		}
	}
	for _, command := range [][]string{
		nil,
		{"submit"},
		{"status", "unexpected"},
	} {
		if err := remoteCommand(command); err == nil {
			t.Fatalf("remote command %v unexpectedly succeeded", command)
		}
	}
}
