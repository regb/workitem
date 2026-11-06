package cli

import (
	"bytes"
	"compress/zlib"
	"encoding/base64"
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
