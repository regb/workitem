package driverprotocol

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestControlContentValidation(t *testing.T) {
	image := ImageContent{MIMEType: "image/png", Data: "YWJj"}
	for _, tc := range []struct {
		name    string
		command ControlCommand
	}{
		{"message too long", ControlCommand{Message: strings.Repeat("x", MaxControlMessageBytes+1)}},
		{"invalid UTF-8", ControlCommand{Message: "\xff"}},
		{"too many images", ControlCommand{Type: CommandPrompt, Images: []ImageContent{image, image, image, image, image}}},
		{"shutdown image", ControlCommand{Type: CommandShutdown, Images: []ImageContent{image}}},
		{"unsupported MIME", ControlCommand{Type: CommandPrompt, Images: []ImageContent{{MIMEType: "image/svg+xml", Data: image.Data}}}},
		{"empty data", ControlCommand{Type: CommandPrompt, Images: []ImageContent{{MIMEType: image.MIMEType}}}},
		{"malformed base64", ControlCommand{Type: CommandPrompt, Images: []ImageContent{{MIMEType: image.MIMEType, Data: "not-base64"}}}},
		{"noncanonical base64", ControlCommand{Type: CommandPrompt, Images: []ImageContent{{MIMEType: image.MIMEType, Data: "YR=="}}}},
		{"base64 newline", ControlCommand{Type: CommandPrompt, Images: []ImageContent{{MIMEType: image.MIMEType, Data: "YW\nJj"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.command.ValidateContent(); err == nil {
				t.Fatal("invalid content accepted")
			}
			if err := SubmitControl(context.Background(), "/nonexistent/control.sock", tc.command); err == nil || strings.Contains(err.Error(), "dial") {
				t.Fatalf("invalid content reached socket: %v", err)
			}
		})
	}
	data := base64.StdEncoding.EncodeToString(make([]byte, MaxControlImageBytes))
	command := ControlCommand{Type: CommandPrompt, Images: []ImageContent{{MIMEType: image.MIMEType, Data: data}}}
	if err := command.ValidateContent(); err != nil {
		t.Fatal("boundary image rejected:", err)
	}
	command.Images = append(command.Images, image)
	if err := command.ValidateContent(); err == nil {
		t.Fatal("aggregate overflow accepted")
	}
	command.Images = []ImageContent{{MIMEType: image.MIMEType, Data: base64.StdEncoding.EncodeToString(make([]byte, MaxControlImageBytes+1))}}
	if err := command.ValidateContent(); err == nil {
		t.Fatal("single-image overflow accepted")
	}
}

func TestControlTransportSupportsImageLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	server, err := ListenControl(path)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	data := base64.StdEncoding.EncodeToString(make([]byte, MaxControlImageBytes))
	command := ControlCommand{ProtocolVersion: RuntimeVersion, ID: "image", RuntimeID: "owner", WorkItemID: "item", Type: CommandPrompt, Message: strings.Repeat("\x00", MaxControlMessageBytes), Images: []ImageContent{{MIMEType: "image/png", Data: data}}}
	done := make(chan error, 1)
	go func() { done <- SubmitControl(ctx, path, command) }()
	select {
	case request := <-server.Requests():
		if len(request.Command.Images) != 1 || request.Command.Images[0].Data != data || request.Command.Message != command.Message {
			t.Error("image command changed in transport")
		}
		request.Respond(nil)
	case <-ctx.Done():
		t.Fatal("large control command not delivered")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestControlServerValidatesDirectClients(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sock")
	server, err := ListenControl(path)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var response ControlResponse
	err = exchangeRuntimeSocket(ctx, path, ControlCommand{Type: CommandPrompt, Images: []ImageContent{{MIMEType: "image/png", Data: "invalid"}}}, &response)
	if err != nil || response.Accepted || !strings.Contains(response.Error, "base64") {
		t.Fatalf("response=%+v err=%v", response, err)
	}
	select {
	case <-server.Requests():
		t.Fatal("invalid command was handed off")
	default:
	}
}

func TestControlSerializedEnvelopeLimit(t *testing.T) {
	command := ControlCommand{Type: CommandAbort, Actor: strings.Repeat("x", MaxControlCommandBytes)}
	err := SubmitControl(context.Background(), "/nonexistent/control.sock", command)
	if err == nil || !strings.Contains(err.Error(), "exceeds 12 MiB") {
		t.Fatalf("oversized envelope reached socket: %v", err)
	}
}

func TestEventLimitRemainsOneMiB(t *testing.T) {
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	go func() { _ = json.NewEncoder(left).Encode(map[string]string{"data": strings.Repeat("x", 1<<20)}) }()
	var event RuntimeEvent
	if err := decodeLine(right, &event, 1<<20); err == nil {
		t.Fatal("oversized event accepted")
	}
}
