package cli

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDaemonBootstrapEnvironment(t *testing.T) {
	var buffer bytes.Buffer
	writer := zlib.NewWriter(&buffer)
	writer.Write([]byte(`{"p":{"PATH":"/usr/bin","RESTORED":"before","SECRET":null},"n":{"PATH":"/project/bin:/usr/bin","RESTORED":null,"SECRET":"secret"}}`))
	writer.Close()
	diff := base64.URLEncoding.EncodeToString(buffer.Bytes())
	got, err := daemonBootstrapEnvironment([]string{"DIRENV_DIFF=" + diff, "DIRENV_DIR=/project", "PATH=/project/bin:/usr/bin", "SECRET=secret", "WI_ITEM=old", "WI_AGENT_DAEMON_SOCKET=/old", "TMUX=old", "TMUX_PANE=%1", "PWD=/project", "OLDPWD=/other", "HOME=/home/test", "SSH_AUTH_SOCK=/ssh", "LANG=en_US.UTF-8", "XDG_DATA_HOME=/data"})
	want := []string{"HOME=/home/test", "LANG=en_US.UTF-8", "PATH=/usr/bin", "RESTORED=before", "SSH_AUTH_SOCK=/ssh", "XDG_DATA_HOME=/data"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v err=%v", got, err)
	}
	if _, err := daemonBootstrapEnvironment([]string{"DIRENV_DIFF=broken"}); err == nil {
		t.Fatal("malformed diff accepted")
	}
	plain, err := daemonBootstrapEnvironment([]string{"HOME=/home/test", "PATH=/bin"})
	if err != nil || !reflect.DeepEqual(plain, []string{"HOME=/home/test", "PATH=/bin"}) {
		t.Fatalf("plain=%v err=%v", plain, err)
	}
}

func TestRemoteDefaultBranchIgnoresRepositoryEnvironment(t *testing.T) {
	repo, unrelated := t.TempDir(), t.TempDir()
	for _, entry := range []struct{ path, branch string }{{repo, "intended"}, {unrelated, "wrong"}} {
		if out, err := exec.Command("git", "init", "--bare", "-b", entry.branch, entry.path).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
	}
	// ls-remote needs an existing HEAD ref.
	seed := t.TempDir()
	for _, args := range [][]string{{"init", "-b", "intended", seed}, {"-C", seed, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "--allow-empty", "-m", "init"}, {"-C", seed, "push", repo, "intended"}} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("%s: %v", out, err)
		}
	}
	t.Setenv("GIT_DIR", unrelated)
	t.Setenv("GIT_WORK_TREE", filepath.Join(unrelated, "work"))
	got, err := remoteDefaultBranch(context.Background(), repo)
	if err != nil || got != "intended" {
		t.Fatalf("branch=%s err=%v", got, err)
	}
}
