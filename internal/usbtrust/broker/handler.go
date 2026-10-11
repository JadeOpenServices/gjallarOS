package broker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"

	"github.com/JadeOpenServices/gjallarOS/internal/usbtrust"
)

const MaxRequestBytes = 16 * 1024

type Reader interface {
	Status(context.Context) (Status, error)
	Audit(context.Context) (usbtrust.AuditResult, uint64, error)
}

type Handler struct {
	OwnerUID uint32
	Reader   Reader
	Mutator  interface {
		Mutate(context.Context, Request) (Response, error)
	}
	OnDecision func(uint32, Request, Response)
}

func (h Handler) Handle(
	ctx context.Context,
	peerUID uint32,
	request Request,
) (response Response) {
	defer func() {
		if h.OnDecision != nil && request.Action.Mutation() {
			h.OnDecision(peerUID, request, response)
		}
	}()
	if err := ValidateRequest(request); err != nil {
		return errorResponse(err)
	}

	if err := AuthorizePeer(
		peerUID,
		request.Action,
		h.OwnerUID,
	); err != nil {
		return errorResponse(err)
	}

	if request.Action.Mutation() {
		if h.Mutator != nil {
			response, err := h.Mutator.Mutate(ctx, request)
			if err != nil {
				return errorResponse(err)
			}
			return response
		}
		return errorResponse(fmt.Errorf(
			"USB trust mutation %q is not enabled",
			request.Action,
		))
	}

	if h.Reader == nil {
		return errorResponse(fmt.Errorf(
			"USB trust read model is unavailable",
		))
	}

	switch request.Action {
	case ActionPolicy:
		reader, ok := h.Reader.(interface {
			Policy(context.Context) ([]usbtrust.Decision, uint64, error)
		})
		if !ok {
			return errorResponse(fmt.Errorf("USB trust policy reader is unavailable"))
		}
		policy, revision, err := reader.Policy(ctx)
		if err != nil {
			return errorResponse(err)
		}
		enforcing := false
		if mode, ok := h.Reader.(interface{ EnforcementEnabled() bool }); ok {
			enforcing = mode.EnforcementEnabled()
		}
		var media []usbtrust.MediaDecision
		if gate, ok := h.Reader.(interface {
			MediaPolicy() []usbtrust.MediaDecision
		}); ok {
			media = gate.MediaPolicy()
		}
		return Response{OK: true, Policy: policy, Media: media, Revision: revision, Enforcing: enforcing}
	case ActionStatus:
		status, err := h.Reader.Status(ctx)
		if err != nil {
			return errorResponse(fmt.Errorf(
				"read USB trust status: %w",
				err,
			))
		}

		return Response{
			OK:        true,
			Enforcing: status.Enforcing,
			Revision:  status.Revision,
			Status:    &status,
		}

	case ActionAudit:
		result, revision, err := h.Reader.Audit(ctx)
		if err != nil {
			return errorResponse(fmt.Errorf(
				"audit USB trust state: %w",
				err,
			))
		}

		return Response{
			OK:       true,
			Revision: revision,
			Audit:    &result,
		}

	default:
		return errorResponse(fmt.Errorf(
			"USB trust action %q has no handler",
			request.Action,
		))
	}
}

func (h Handler) ServeConn(
	ctx context.Context,
	conn *net.UnixConn,
) error {
	if conn == nil {
		return fmt.Errorf("USB trust connection is nil")
	}

	peerUID, err := PeerUID(conn)
	if err != nil {
		return err
	}

	request, err := readBoundedRequest(conn)
	if err != nil {
		return EncodeResponse(
			conn,
			errorResponse(err),
		)
	}

	return EncodeResponse(
		conn,
		h.Handle(ctx, peerUID, request),
	)
}

func readBoundedRequest(
	reader io.Reader,
) (Request, error) {
	limited := &io.LimitedReader{
		R: reader,
		N: MaxRequestBytes + 1,
	}

	data, err := io.ReadAll(limited)
	if err != nil {
		return Request{}, fmt.Errorf(
			"read USB trust request: %w",
			err,
		)
	}

	if len(data) > MaxRequestBytes {
		return Request{}, fmt.Errorf(
			"USB trust request exceeds %d bytes",
			MaxRequestBytes,
		)
	}

	if len(bytes.TrimSpace(data)) == 0 {
		return Request{}, fmt.Errorf(
			"USB trust request is empty",
		)
	}

	return DecodeRequest(bytes.NewReader(data))
}

func errorResponse(err error) Response {
	return Response{
		OK:    false,
		Error: err.Error(),
	}
}
