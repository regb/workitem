// Package executortest supplies an isolated real SSH server for opt-in tests.
package executortest

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type Server struct{ Host, Config, Root string }

func SSHServer(t *testing.T) Server {
	t.Helper()
	if os.Getenv("WI_TEST_SSH") != "1" {
		t.Skip("set WI_TEST_SSH=1 with wi-executor-ssh-test image available")
	}
	dir, err := os.MkdirTemp("/tmp", "wi-ssh-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	run := func(name string, args ...string) string {
		t.Helper()
		out, err := exec.Command(name, args...).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %s %v", name, out, err)
		}
		return strings.TrimSpace(string(out))
	}
	key := filepath.Join(dir, "client")
	hostKey := filepath.Join(dir, "host")
	run("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key)
	run("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", hostKey)
	write := func(name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	public, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	write("authorized_keys", string(public))
	write("sshd_config", "Port 22\nHostKey /keys/host\nAuthorizedKeysFile /keys/authorized_keys\nPasswordAuthentication no\nKbdInteractiveAuthentication no\nPermitRootLogin no\nAllowUsers wi\nStrictModes no\nLogLevel ERROR\n")
	name := fmt.Sprintf("wi-ssh-test-%d", time.Now().UnixNano())
	run("docker", "create", "--name", name, "-p", "127.0.0.1::22", "wi-executor-ssh-test")
	t.Cleanup(func() {
		out, err := exec.Command("docker", "rm", "-f", name).CombinedOutput()
		if err != nil {
			t.Errorf("SSH cleanup: %s %v", out, err)
		}
	})
	// sshd must read the public key through /keys. Host private keys remain root-only.
	if err := os.Chmod(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "authorized_keys"), 0644); err != nil {
		t.Fatal(err)
	}
	run("docker", "cp", dir+"/.", name+":/keys")
	run("docker", "start", name)
	address := run("docker", "port", name, "22/tcp")
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	hostPublic, err := os.ReadFile(hostKey + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	write("known_hosts", "[127.0.0.1]:"+port+" "+string(hostPublic))
	config := filepath.Join(dir, "ssh_config")
	write("ssh_config", fmt.Sprintf("Host wi-test\n HostName 127.0.0.1\n Port %s\n User wi\n IdentityFile %s\n UserKnownHostsFile %s\n IdentitiesOnly yes\n", port, key, filepath.Join(dir, "known_hosts")))
	deadline := time.Now().Add(10 * time.Second)
	for {
		out, err := exec.Command("ssh", "-F", config, "-oBatchMode=yes", "-oStrictHostKeyChecking=yes", "wi-test", "true").CombinedOutput()
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("SSH readiness: %s %v", out, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return Server{Host: "wi-test", Config: config, Root: "/home/wi/executors"}
}
