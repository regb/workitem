package cli

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/regb/workitem/internal/coordinator"
	"github.com/regb/workitem/internal/model"
)

func TestRepositoryListAlignsColumns(t *testing.T) {
	repos := []coordinator.RepositoryView{
		{OwnedRepository: model.OwnedRepository{Name: "fleet", IntegrationBranch: "main", ID: "first"}},
		{OwnedRepository: model.OwnedRepository{Name: "travel-topology-monorepo", IntegrationBranch: "master", ID: "second"}},
		{OwnedRepository: model.OwnedRepository{Name: "workitem", IntegrationBranch: "release/integration", ID: "third"}},
	}
	var output bytes.Buffer
	if err := writeRepositoryList(&output, repos); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "\t") {
		t.Fatalf("raw tabs in output: %q", output.String())
	}
	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if len(lines) != len(repos)+1 {
		t.Fatalf("lines = %q", lines)
	}
	branchColumn := strings.Index(lines[0], "INTEGRATION")
	lines = lines[1:]
	for index, repo := range repos {
		if strings.Index(lines[index], repo.IntegrationBranch) != branchColumn {
			t.Fatalf("unaligned columns: %q", output.String())
		}
		if fields := strings.Fields(lines[index]); len(fields) != 4 || fields[0] != repo.Name || fields[1] != repo.IntegrationBranch || fields[2] != "active" || fields[3] != "untracked" {
			t.Fatalf("row = %q", lines[index])
		}
	}
}

func TestRepositoryListEmpty(t *testing.T) {
	var output bytes.Buffer
	if err := writeRepositoryList(&output, nil); err != nil || output.Len() != 0 {
		t.Fatalf("empty list = %q, %v", output.String(), err)
	}
}

type failedRepositoryListWriter struct{ err error }

func (writer failedRepositoryListWriter) Write([]byte) (int, error) { return 0, writer.err }

func TestRepositoryListReportsWriteFailure(t *testing.T) {
	want := errors.New("output unavailable")
	err := writeRepositoryList(failedRepositoryListWriter{want}, []coordinator.RepositoryView{{OwnedRepository: model.OwnedRepository{Name: "fleet", IntegrationBranch: "main", ID: "first"}}})
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
}
