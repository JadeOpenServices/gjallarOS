package main

import (
	"strings"
	"testing"
)

func TestBlockFallback(t *testing.T) {
	got, err := blockFallback([]byte("RuleFile=/x\nImplicitPolicyTarget=allow\nPresentDevicePolicy=apply-policy\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := "RuleFile=/x\nImplicitPolicyTarget=block\nPresentDevicePolicy=apply-policy\n"
	if string(got) != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBlockFallbackNeedsTheLine(t *testing.T) {
	if _, err := blockFallback([]byte("RuleFile=/x\n")); err == nil || !strings.Contains(err.Error(), "ImplicitPolicyTarget") {
		t.Fatalf("err = %v", err)
	}
}
