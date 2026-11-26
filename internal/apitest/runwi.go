package apitest

// RunAsWi tells a test binary to run as the wi executable: a fixture that
// runs agents sets it for the runtime hosts and drivers its daemon starts
// from the test binary, whose TestMain is runwi.Main. It is not a WI_
// variable, which the daemon keeps out of the environments it starts.
const RunAsWi = "APITEST_RUN_AS_WI"
