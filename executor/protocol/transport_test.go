package protocol

import (
	"context"
	"io"
	"testing"
)

func TestExchangeRoundTrip(t *testing.T) {
	requestReader, requestWriter := io.Pipe()
	responseReader, responseWriter := io.Pipe()
	serverDone := make(chan error, 1)
	go func() {
		request, err := ReadRequest(requestReader)
		if err == nil && request.Operation != OperationDoctor {
			t.Errorf("operation=%q", request.Operation)
		}
		if err == nil {
			err = WriteResponse(responseWriter, Response{Doctor: &DoctorResult{WorkerID: "worker"}})
		}
		_ = responseWriter.Close()
		serverDone <- err
	}()
	response, err := Exchange(context.Background(), requestWriter, responseReader, Request{ProtocolVersion: Version, Operation: OperationDoctor, MasterID: "master"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Doctor == nil || response.Doctor.WorkerID != "worker" {
		t.Fatalf("response=%+v", response)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestReadRequestRejectsProtocolMismatch(t *testing.T) {
	reader, writer := io.Pipe()
	go func() {
		_, _ = writer.Write([]byte(`{"protocol_version":99,"operation":"doctor","master_id":"master"}`))
		_ = writer.Close()
	}()
	if _, err := ReadRequest(reader); err == nil {
		t.Fatal("expected protocol mismatch")
	}
}
