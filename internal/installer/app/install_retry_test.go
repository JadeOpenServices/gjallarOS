package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/baremetalinstall"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/config"
	"github.com/JadeOpenServices/gjallarOS/internal/installer/prompt"
)

func TestRetryInstallAsksOnlyForNixOSInstallFailures(t *testing.T) {
	installErr := fmt.Errorf("install fresh: %w", &baremetalinstall.NixOSInstallError{Err: errors.New("nixos-install failed: exit status 1")})
	checkErr := errors.New("verify target root: not mounted")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	for _, tc := range []struct {
		name   string
		ctx    context.Context
		ask    bool
		answer bool
		err    error
		want   bool
		asked  bool
	}{
		{name: "nobody at the install", ctx: context.Background(), err: installErr},
		{name: "check before nixos-install", ctx: context.Background(), ask: true, answer: true, err: checkErr},
		{name: "run stopped", ctx: cancelled, ask: true, answer: true, err: installErr},
		{name: "person tries again", ctx: context.Background(), ask: true, answer: true, err: installErr, want: true, asked: true},
		{name: "person stops", ctx: context.Background(), ask: true, err: installErr, asked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			asked := false
			var retry askRetry
			if tc.ask {
				retry = func(context.Context, error) bool {
					asked = true
					return tc.answer
				}
			}
			if got := retryInstall(tc.ctx, retry, tc.err); got != tc.want {
				t.Fatalf("retryInstall = %v, want %v", got, tc.want)
			}
			if asked != tc.asked {
				t.Fatalf("asked = %v, want %v", asked, tc.asked)
			}
		})
	}
}

func TestAskInstallRetryAnswers(t *testing.T) {
	for input, want := range map[string]bool{
		"\n":          true,
		"Try again\n": true,
		"1\n":         true,
		"Stop\n":      false,
		"2\n":         false,
		"":            false,
	} {
		var out bytes.Buffer
		ui := prompt.New(strings.NewReader(input), &out)
		got := askInstallRetry(context.Background(), ui, &out, errors.New("nixos-install failed: exit status 1"))
		if got != want {
			t.Errorf("input %q: again = %v, want %v\n%s", input, got, want, out.String())
		}
		if !strings.Contains(out.String(), "nixos-install failed: exit status 1") {
			t.Errorf("input %q: error not shown:\n%s", input, out.String())
		}
	}
}

func TestInstallRetryNeedsAPerson(t *testing.T) {
	var out bytes.Buffer
	ui := prompt.New(strings.NewReader(""), &out)
	if installRetry(strings.NewReader(""), ui, &out, config.User{}) != nil {
		t.Fatal("installRetry offered a retry without a terminal")
	}
}
