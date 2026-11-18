package move

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/JadeOpenServices/gjallarOS/internal/installer/move/oldroot"
	"github.com/JadeOpenServices/gjallarOS/internal/movehelper"
)

// fakeMode makes the test binary act as the move helper, so ExecHelper runs
// a real program without a shell.
const fakeMode = "GJALLAR_TEST_FAKE_MOVE_HELPER"

func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeMode); mode != "" {
		fakeHelperMain(mode)
		return
	}
	os.Exit(m.Run())
}

func fakeHelperMain(mode string) {
	var req movehelper.Request
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
		fmt.Fprintln(os.Stderr, "bad request:", err)
		os.Exit(1)
	}
	switch mode {
	case "echo":
		json.NewEncoder(os.Stdout).Encode(movehelper.OpenResult{
			Info:     oldroot.Info{Channel: req.Op + " " + req.Disk + " " + string(req.Key)},
			PINToken: true,
		})
	case "refuse":
		json.NewEncoder(os.Stdout).Encode(movehelper.RefusalResult{Refusal: &oldroot.Refusal{
			Reason: oldroot.Managed, Text: "Managed text.", Detail: "management.enable is on",
		}})
	case "fail":
		fmt.Fprint(os.Stderr, "first line\n\x1b[31mopen \x1b]0;title\x07failed\x00 here"+strings.Repeat("x", 300)+"\n")
		os.Exit(1)
	case "env":
		json.NewEncoder(os.Stdout).Encode(oldroot.Info{Channel: strings.Join(os.Environ(), "\n")})
	case "silent":
		os.Exit(3)
	case "big":
		os.Stdout.Write([]byte(`{"channel":"` + strings.Repeat("a", maxResult) + `"}`))
	}
}

func fake(t *testing.T, mode string) ExecHelper {
	t.Helper()
	old := helperEnv
	helperEnv = append(slices.Clip(old), fakeMode+"="+mode)
	t.Cleanup(func() { helperEnv = old })
	return ExecHelper{Argv: []string{os.Args[0], "-test.run=^$"}}
}

// L114: a bare or relative helper name is refused before anything runs,
// and the caller's environment does not reach the helper.
func TestExecHelperFixedPathAndEnv(t *testing.T) {
	for _, argv0 := range []string{"gjallar-move-helper", "./gjallar-move-helper"} {
		err := ExecHelper{Argv: []string{argv0}}.Run(context.Background(), movehelper.Request{Op: "open"}, nil)
		if err == nil || !strings.Contains(err.Error(), "not an absolute path") {
			t.Errorf("%s: %v", argv0, err)
		}
	}
	h := fake(t, "env")
	t.Setenv("DM_DEV_DIR", "/run/example/fake-dev")
	t.Setenv("LD_PRELOAD", "/run/example/evil.so")
	var got oldroot.Info
	if err := h.Run(context.Background(), movehelper.Request{Op: "open"}, &got); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Channel, "DM_DEV_DIR") || strings.Contains(got.Channel, "LD_PRELOAD") || !strings.Contains(got.Channel, "LC_ALL=C") {
		t.Errorf("helper env %q", got.Channel)
	}
}

func TestExecHelperSendsRequestOnStdin(t *testing.T) {
	var got movehelper.OpenResult
	req := movehelper.Request{Op: "open", Disk: "/dev/vdb", Key: []byte("k1")}
	if err := fake(t, "echo").Run(context.Background(), req, &got); err != nil {
		t.Fatal(err)
	}
	if got.Channel != "open /dev/vdb k1" || !got.PINToken {
		t.Fatalf("got %+v", got)
	}
}

func TestExecHelperRefusal(t *testing.T) {
	var got movehelper.OpenResult
	err := fake(t, "refuse").Run(context.Background(), movehelper.Request{Op: "open"}, &got)
	r, ok := oldroot.AsRefusal(err)
	if !ok {
		t.Fatalf("err = %v", err)
	}
	if r.Reason != oldroot.Managed || r.Text != "Managed text." {
		t.Fatalf("refusal = %+v", r)
	}
}

func TestExecHelperFailureIsCleaned(t *testing.T) {
	err := fake(t, "fail").Run(context.Background(), movehelper.Request{Op: "check"}, nil)
	if err == nil {
		t.Fatal("a failed helper returned no error")
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "move helper check: ") || !strings.Contains(msg, "failed") {
		t.Fatalf("err = %q", msg)
	}
	if strings.Contains(msg, "first line") {
		t.Errorf("not the last line: %q", msg)
	}
	for _, r := range msg {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("control byte %#x left in %q", r, msg)
		}
	}
	if !strings.HasSuffix(msg, "…") || len([]rune(strings.TrimPrefix(msg, "move helper check: "))) != 201 {
		t.Errorf("not capped at 200: %q", msg)
	}
}

func TestExecHelperSilentFailure(t *testing.T) {
	err := fake(t, "silent").Run(context.Background(), movehelper.Request{Op: "close"}, nil)
	if err == nil || !strings.Contains(err.Error(), "exit status 3") {
		t.Fatalf("err = %v", err)
	}
}

func TestExecHelperAnswerCap(t *testing.T) {
	var got movehelper.OpenResult
	err := fake(t, "big").Run(context.Background(), movehelper.Request{Op: "open"}, &got)
	if err == nil || !strings.Contains(err.Error(), "answer is over") {
		t.Fatalf("err = %v", err)
	}
	if got.Channel != "" {
		t.Error("an over-long answer was read")
	}
}

func TestExecHelperNoArgv(t *testing.T) {
	if err := (ExecHelper{}).Run(context.Background(), movehelper.Request{Op: "find"}, nil); err == nil {
		t.Fatal("ran without a helper")
	}
}

func TestDecodeNilResult(t *testing.T) {
	if err := decode([]byte(`{}`), nil); err != nil {
		t.Fatal(err)
	}
	if err := decode([]byte(`not json`), nil); err == nil {
		t.Fatal("bad JSON accepted")
	}
	var r *oldroot.Refusal
	if err := decode([]byte(`{"refusal":{"reason":"busy","text":"t"}}`), nil); !errors.As(err, &r) {
		t.Fatalf("refusal lost with no result: %v", err)
	}
}
