package broker

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeReadOnlyRequest(t *testing.T) {
	request, err := DecodeRequest(
		strings.NewReader(`{"action":"audit"}`),
	)
	if err != nil {
		t.Fatal(err)
	}

	if request.Action != ActionAudit {
		t.Fatalf(
			"action = %q, want %q",
			request.Action,
			ActionAudit,
		)
	}
}

func TestDecodeRejectsUnknownField(t *testing.T) {
	_, err := DecodeRequest(
		strings.NewReader(
			`{"action":"audit","magicTrustEverything":true}`,
		),
	)

	if err == nil {
		t.Fatal("accepted unknown broker request field")
	}
}

func TestDecodeRejectsTrailingJSON(t *testing.T) {
	_, err := DecodeRequest(
		strings.NewReader(
			`{"action":"audit"} {"action":"status"}`,
		),
	)

	if err == nil {
		t.Fatal("accepted trailing broker request")
	}
}

func TestPermanentTrustRequiresPortableDecision(t *testing.T) {
	_, err := DecodeRequest(
		strings.NewReader(
			`{"action":"trust-permanent","runtimeId":"7","connection":"review-token"}`,
		),
	)

	if err == nil {
		t.Fatal(
			"permanent trust accepted without portability decision",
		)
	}
}

func TestPermanentTrustAcceptsExplicitPortableDecision(
	t *testing.T,
) {
	portable := false

	request := Request{
		Action:     ActionTrustPermanent,
		RuntimeID:  "7",
		Connection: "review-token",
		Portable:   &portable,
	}

	if err := ValidateRequest(request); err != nil {
		t.Fatal(err)
	}
}

func TestConnectionBoundMutationsRequireReviewToken(t *testing.T) {
	portable := false
	for _, request := range []Request{
		{Action: ActionAllowOnce, RuntimeID: "7"},
		{Action: ActionKeepBlocked, RuntimeID: "7"},
		{Action: ActionTrustPermanent, RuntimeID: "7", Portable: &portable},
		{Action: ActionEnrollInternal, RuntimeID: "7", Role: "hardware.input"},
		{Action: ActionAcceptReplacement, RuntimeID: "7", TrustedID: "device:old"},
	} {
		if err := ValidateRequest(request); err == nil {
			t.Fatalf("%s accepted without current connection token", request.Action)
		}
	}
}

func TestOwnerMayReadButNotMutate(t *testing.T) {
	const ownerUID uint32 = 1000

	if err := AuthorizePeer(
		ownerUID,
		ActionAudit,
		ownerUID,
	); err != nil {
		t.Fatalf("owner read denied: %v", err)
	}

	if err := AuthorizePeer(
		ownerUID,
		ActionTrustPermanent,
		ownerUID,
	); err == nil {
		t.Fatal("owner mutation authorized without privilege")
	}
}

func TestRootMayMutate(t *testing.T) {
	if err := AuthorizePeer(
		0,
		ActionTrustPermanent,
		1000,
	); err != nil {
		t.Fatalf("root mutation denied: %v", err)
	}
}

func TestUnrelatedUIDDenied(t *testing.T) {
	if err := AuthorizePeer(
		2000,
		ActionStatus,
		1000,
	); err == nil {
		t.Fatal("unrelated UID received USB trust access")
	}
}

func TestResponseEncoding(t *testing.T) {
	var output bytes.Buffer

	if err := EncodeResponse(
		&output,
		Response{
			OK:      true,
			Message: "test",
		},
	); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(output.String(), `"ok":true`) {
		t.Fatalf(
			"unexpected response %q",
			output.String(),
		)
	}
}

func TestPeerUIDUsesUnixPeerCredentials(t *testing.T) {
	socket := filepath.Join(
		t.TempDir(),
		"broker.sock",
	)

	listener, err := net.ListenUnix(
		"unix",
		&net.UnixAddr{
			Name: socket,
			Net:  "unix",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	type acceptResult struct {
		conn *net.UnixConn
		err  error
	}

	accepted := make(chan acceptResult, 1)

	go func() {
		conn, err := listener.AcceptUnix()
		accepted <- acceptResult{
			conn: conn,
			err:  err,
		}
	}()

	client, err := net.DialUnix(
		"unix",
		nil,
		&net.UnixAddr{
			Name: socket,
			Net:  "unix",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	result := <-accepted
	if result.err != nil {
		t.Fatal(result.err)
	}
	defer result.conn.Close()

	got, err := PeerUID(result.conn)
	if err != nil {
		t.Fatal(err)
	}

	want := uint32(os.Geteuid())

	if got != want {
		t.Fatalf(
			"peer UID = %d, want %d",
			got,
			want,
		)
	}
}

func TestAllowMediaValidation(t *testing.T) {
	valid := Request{Action: ActionAllowMedia, Media: "6:0:0:0", Connection: "token"}
	if err := ValidateRequest(valid); err != nil {
		t.Fatalf("valid allow-media rejected: %v", err)
	}
	for name, request := range map[string]Request{
		"no media":            {Action: ActionAllowMedia, Connection: "token"},
		"no connection":       {Action: ActionAllowMedia, Media: "6:0:0:0"},
		"runtime id":          {Action: ActionAllowMedia, Media: "6:0:0:0", Connection: "token", RuntimeID: "1"},
		"trusted id":          {Action: ActionAllowMedia, Media: "6:0:0:0", Connection: "token", TrustedID: "x"},
		"media on allow-once": {Action: ActionAllowOnce, RuntimeID: "1", Connection: "token", Media: "6:0:0:0"},
		"media on policy":     {Action: ActionPolicy, Media: "6:0:0:0"},
	} {
		if err := ValidateRequest(request); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := AuthorizePeer(1000, ActionAllowMedia, 1000); err == nil {
		t.Fatal("owner may not open media without root")
	}
}
