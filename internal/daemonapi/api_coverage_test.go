package daemonapi_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
)

// clientServices names the service of each field of apitransport.Clients.
var clientServices = map[string]string{
	"Items":        "ItemService",
	"Lifecycle":    "LifecycleService",
	"Workspaces":   "WorkspaceService",
	"Reviews":      "ReviewService",
	"Agents":       "AgentService",
	"Control":      "ControlService",
	"Repositories": "RepositoryService",
	"Schedules":    "ScheduleService",
	"Profiles":     "AgentProfileService",
	"Jobs":         "JobService",
	"Events":       "EventService",
	"System":       "SystemService",
	"Views":        "ViewService",
}

// uncovered are the procedures the black-box suite does not call, each with
// the reason. Keep it short: a procedure belongs here only when calling it
// from the suite would prove nothing, or cannot be done in a test process.
var uncovered = map[string]string{}

// Every procedure of the contract is called by the black-box suite (the
// api_*_test.go files, through the generated clients), or is waived above
// with a reason. Adding an RPC forces a test of it, or a decision not to.
func TestEveryProcedureIsCoveredByTheSuite(t *testing.T) {
	_ = v1.File_wi_core_v1_items_proto
	files, err := filepath.Glob("api_*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	called := map[string]bool{}
	pattern := regexp.MustCompile(`\.(` + fieldAlternatives() + `)\.([A-Z][A-Za-z]+)[(,]`)
	for _, file := range files {
		if file == "api_coverage_test.go" {
			continue
		}
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range pattern.FindAllSubmatch(source, -1) {
			called["/wi.core.v1."+clientServices[string(match[1])]+"/"+string(match[2])] = true
		}
	}
	all := map[string]bool{}
	protoregistry.GlobalFiles.RangeFilesByPackage("wi.core.v1", func(file protoreflect.FileDescriptor) bool {
		services := file.Services()
		for i := range services.Len() {
			methods := services.Get(i).Methods()
			for j := range methods.Len() {
				all["/"+string(services.Get(i).FullName())+"/"+string(methods.Get(j).Name())] = true
			}
		}
		return true
	})
	var missing []string
	for procedure := range all {
		_, waived := uncovered[procedure]
		switch {
		case called[procedure] && waived:
			t.Errorf("%s is waived but the suite calls it: remove the waiver", procedure)
		case !called[procedure] && !waived:
			missing = append(missing, procedure)
		}
	}
	for procedure := range uncovered {
		if !all[procedure] {
			t.Errorf("%s is waived but the API does not have it", procedure)
		}
	}
	sort.Strings(missing)
	for _, procedure := range missing {
		t.Errorf("%s is not called by the black-box suite: test it in an api_*_test.go file, or waive it with a reason", procedure)
	}
	t.Logf("%d of %d procedures called, %d waived", len(all)-len(missing)-len(uncovered), len(all), len(uncovered))
}

func fieldAlternatives() string {
	fields := make([]string, 0, len(clientServices))
	for field := range clientServices {
		fields = append(fields, field)
	}
	return strings.Join(fields, "|")
}
