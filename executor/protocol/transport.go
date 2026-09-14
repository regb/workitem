package protocol

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
)

func Exchange(ctx context.Context, input io.WriteCloser, output io.Reader, request Request) (Response, error) {
	if request.ProtocolVersion == 0 {
		request.ProtocolVersion = Version
	}
	if err := json.NewEncoder(input).Encode(request); err != nil {
		_ = input.Close()
		return Response{}, fmt.Errorf("send executor request: %w", err)
	}
	if err := input.Close(); err != nil {
		return Response{}, fmt.Errorf("close executor request: %w", err)
	}
	type result struct {
		response Response
		err      error
	}
	ready := make(chan result, 1)
	go func() {
		var response Response
		err := json.NewDecoder(bufio.NewReader(output)).Decode(&response)
		ready <- result{response: response, err: err}
	}()
	select {
	case <-ctx.Done():
		return Response{}, ctx.Err()
	case decoded := <-ready:
		if decoded.err != nil {
			return Response{}, fmt.Errorf("read executor response: %w", decoded.err)
		}
		if decoded.response.ProtocolVersion != Version {
			return Response{}, fmt.Errorf("executor response protocol %d is incompatible with %d", decoded.response.ProtocolVersion, Version)
		}
		if decoded.response.Error != "" {
			return decoded.response, fmt.Errorf("executor: %s", decoded.response.Error)
		}
		return decoded.response, nil
	}
}

func ReadRequest(r io.Reader) (Request, error) {
	var request Request
	if err := json.NewDecoder(bufio.NewReader(io.LimitReader(r, 600<<20))).Decode(&request); err != nil {
		return Request{}, fmt.Errorf("read executor request: %w", err)
	}
	if request.ProtocolVersion != Version {
		return Request{}, fmt.Errorf("executor request protocol %d is incompatible with %d", request.ProtocolVersion, Version)
	}
	return request, nil
}

func WriteResponse(w io.Writer, response Response) error {
	response.ProtocolVersion = Version
	return json.NewEncoder(w).Encode(response)
}
