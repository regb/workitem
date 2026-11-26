package testutil

import "os"

const helperProcessVariable = "GO_TEST_HELPER_PROCESS"

// HelperProcess returns a command that re-runs the current test binary as the
// test function named helper, and an environment holding extra plus what marks
// the child as that helper and keeps it in the parent's isolated test root.
// Arguments appended to the command follow "--", so the test binary ignores
// them. The helper test starts with `if !testutil.InHelperProcess(name) { return }`.
func HelperProcess(helper string, extra map[string]string) ([]string, map[string]string) {
	env := map[string]string{helperProcessVariable: helper, testProcessRoot: os.Getenv(testProcessRoot)}
	for key, value := range extra {
		env[key] = value
	}
	return []string{os.Args[0], "-test.run=^" + helper + "$", "--"}, env
}

// InHelperProcess reports whether this process was started as helper.
func InHelperProcess(helper string) bool { return os.Getenv(helperProcessVariable) == helper }
