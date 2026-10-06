package workspacetool

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	SocketEnvironment = "WI_WORKSPACE_TOOL_SOCKET"
	TargetEnvironment = "WI_WORKSPACE_TARGET"
	CWDEnvironment    = "WI_WORKSPACE_REMOTE_CWD"
)

func RunRuntime(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) (int, error) {
	socket := strings.TrimSpace(os.Getenv(SocketEnvironment))
	target := strings.TrimSpace(os.Getenv(TargetEnvironment))
	remoteCWD := strings.TrimSpace(os.Getenv(CWDEnvironment))
	if socket == "" || target == "" || remoteCWD == "" {
		return 1, fmt.Errorf("remote workspace runtime environment is incomplete")
	}
	if len(args) < 3 || args[0] != "container" {
		return 2, fmt.Errorf("usage: wi internal-workspace-runtime container inspect|exec ...")
	}
	switch args[1] {
	case "inspect":
		formatted := false
		index := 2
		if index < len(args) && args[index] == "-f" {
			if index+2 >= len(args) {
				return 2, fmt.Errorf("container inspect format and target are required")
			}
			formatted = true
			index += 2
		}
		if index != len(args)-1 || args[index] != target {
			return 1, fmt.Errorf("remote workspace target does not match this runtime")
		}
		response, err := Exchange(ctx, socket, Request{CWD: remoteCWD, Command: []string{"test", "-d", remoteCWD}})
		if err != nil {
			return 1, err
		}
		if response.ExitCode != 0 {
			_, _ = stderr.Write(response.Stderr)
			return response.ExitCode, nil
		}
		if formatted {
			_, _ = io.WriteString(stdout, "true\n")
		} else {
			_, _ = io.WriteString(stdout, "[]\n")
		}
		return 0, nil
	case "exec":
		index := 2
		cwd := remoteCWD
		readStdin := false
		environment := map[string]string{}
		for index < len(args) {
			switch args[index] {
			case "-i":
				readStdin = true
				index++
			case "-w":
				if index+1 >= len(args) {
					return 2, fmt.Errorf("container exec working directory is missing")
				}
				cwd = args[index+1]
				index += 2
			case "-e":
				if index+1 >= len(args) {
					return 2, fmt.Errorf("container exec environment entry is missing")
				}
				key, value, ok := strings.Cut(args[index+1], "=")
				if !ok || key == "" {
					return 2, fmt.Errorf("container exec environment entry must be KEY=VALUE")
				}
				environment[key] = value
				index += 2
			default:
				goto optionsDone
			}
		}
	optionsDone:
		if index >= len(args) || args[index] != target {
			return 1, fmt.Errorf("remote workspace target does not match this runtime")
		}
		index++
		if index >= len(args) {
			return 2, fmt.Errorf("container exec command is missing")
		}
		var input []byte
		if readStdin {
			var err error
			input, err = io.ReadAll(io.LimitReader(stdin, (16<<20)+1))
			if err != nil {
				return 1, err
			}
			if len(input) > 16<<20 {
				return 1, fmt.Errorf("remote workspace stdin exceeds 16 MiB")
			}
		}
		response, err := Exchange(ctx, socket, Request{CWD: cwd, Command: append([]string(nil), args[index:]...), Stdin: input, Environment: environment})
		if err != nil {
			return 1, err
		}
		_, _ = stdout.Write(response.Stdout)
		_, _ = stderr.Write(response.Stderr)
		if response.Truncated {
			_, _ = io.WriteString(stderr, "\nwi: remote workspace output exceeded the transport limit\n")
		}
		return response.ExitCode, nil
	default:
		return 2, fmt.Errorf("unsupported workspace runtime operation %q", args[1])
	}
}
