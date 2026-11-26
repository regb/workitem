package daemonapi

import (
	"strings"

	v1 "github.com/regb/workitem/gen/wi/core/v1"
)

// The review core names its values in lower case, such as "old" or
// "request_changes"; the API's enums name them REVIEW_SIDE_OLD or
// APPROVAL_KIND_REQUEST_CHANGES. These convert between the two: an unknown
// value is unspecified one way and empty the other, which the core refuses
// where a value is required.

func toEnum[E ~int32](names map[string]int32, prefix, value string) E {
	if value == "" {
		return 0
	}
	return E(names[prefix+strings.ToUpper(value)])
}

func fromEnum[E ~int32](names map[int32]string, prefix string, value E) string {
	if value == 0 {
		return ""
	}
	name, ok := names[int32(value)]
	if !ok {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(name, prefix))
}

func revisionTrigger(value string) v1.RevisionTrigger {
	return toEnum[v1.RevisionTrigger](v1.RevisionTrigger_value, "REVISION_TRIGGER_", value)
}
func reviewSide(value string) v1.ReviewSide {
	return toEnum[v1.ReviewSide](v1.ReviewSide_value, "REVIEW_SIDE_", value)
}
func coreSide(value v1.ReviewSide) string {
	return fromEnum(v1.ReviewSide_name, "REVIEW_SIDE_", value)
}
func reviewRole(value string) v1.ReviewRole {
	return toEnum[v1.ReviewRole](v1.ReviewRole_value, "REVIEW_ROLE_", value)
}
func threadResolution(value string) v1.ThreadResolution {
	return toEnum[v1.ThreadResolution](v1.ThreadResolution_value, "THREAD_RESOLUTION_", value)
}
func coreResolution(value v1.ThreadResolution) string {
	return fromEnum(v1.ThreadResolution_name, "THREAD_RESOLUTION_", value)
}
func threadStatus(value string) v1.ThreadStatus {
	return toEnum[v1.ThreadStatus](v1.ThreadStatus_value, "THREAD_STATUS_", value)
}
func approvalKind(value string) v1.ApprovalKind {
	return toEnum[v1.ApprovalKind](v1.ApprovalKind_value, "APPROVAL_KIND_", value)
}
func coreApprovalKind(value v1.ApprovalKind) string {
	return fromEnum(v1.ApprovalKind_name, "APPROVAL_KIND_", value)
}
func fileChange(value string) v1.FileChange {
	return toEnum[v1.FileChange](v1.FileChange_value, "FILE_CHANGE_", value)
}
func branchKind(value string) v1.BranchKind {
	return toEnum[v1.BranchKind](v1.BranchKind_value, "BRANCH_KIND_", value)
}
