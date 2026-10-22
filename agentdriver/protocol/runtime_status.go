package driverprotocol

const EventRuntimeStatus = "runtime.status"

// Status facts are closed vocabularies, never native UI, prompt or error text.
func ValidReadiness(value string) bool {
	switch value {
	case "", "starting", "ready", "busy", "awaiting_confirmation", "waiting_input", "blocked", "stopping":
		return true
	}
	return false
}
func ValidDeliveryState(value string) bool {
	switch value {
	case "", "queued", "submitted", "confirmed", "not_sent", "uncertain", "cancelled":
		return true
	}
	return false
}

// AwaitsAnswer reports a waiting_input reason for a turn paused on a
// question, plan approval or permission prompt that only the operator can
// answer.
func AwaitsAnswer(code string) bool {
	switch code {
	case "question_pending", "plan_approval_pending", "permission_pending":
		return true
	}
	return false
}

func RuntimeReasonText(code string) string {
	switch code {
	case "runtime_stopping":
		return "Claude is stopping"
	case "headless_input_unavailable":
		return "Claude native input is unavailable; restart before further delivery"
	case "headless_initializing":
		return "Waiting for Claude native JSON control initialization"
	case "headless_ready":
		return "Claude native headless control is ready"
	case "headless_protocol_error":
		return "Claude headless protocol failed; delivery may be uncertain"
	case "interaction_required":
		return "Interactive approval or user input is unavailable in headless mode"
	case "native_auth_failed":
		return "Claude authentication or account access failed; inspect its private runtime output"
	case "usage_limit":
		return "Usage limit reached; the turn stopped until it resets"
	case "native_api_failed":
		return "Claude's model or API request failed"
	case "native_turn_interrupted":
		return "Claude's turn was interrupted"
	case "native_queued_cancelled":
		return "Claude cancelled queued input before execution"
	case "native_command_rejected":
		return "Claude rejected or cancelled submitted input"
	case "runtime_starting":
		return "Claude is starting"
	case "session_start_pending":
		return "Waiting for Claude SessionStart; inspect its TUI for setup or disabled hooks"
	case "editor_not_visible":
		return "Claude chat editor is not visible; inspect its TUI"
	case "question_pending":
		return "Claude is waiting for your answer"
	case "plan_approval_pending":
		return "Claude is waiting for plan approval"
	case "permission_pending":
		return "Claude is waiting for permission"
	case "editor_not_empty":
		return "Claude editor contains a draft or dialog; no input sent"
	case "controls_unavailable":
		return "Claude terminal controls are unavailable"
	case "bindings_unavailable":
		return "Claude keybindings cannot be resolved safely"
	case "bindings_changed":
		return "Claude keybindings changed; restart the runtime"
	case "pane_unavailable":
		return "Claude pane inspection failed"
	case "pane_changed":
		return "Claude pane ownership or input state changed"
	case "editor_ready":
		return "Claude chat editor is ready"
	case "execution_pending":
		return "Native prompt received; execution has not yet been observed"
	case "prompt_blocked":
		return "Claude blocked the prompt through a user hook; no model execution"
	case "active_turn":
		return "Claude reports active work"
	case "paste_readback_missing":
		return "Pasted draft was not confirmed; submission shortcut not sent; do not resend automatically"
	case "paste_uncertain":
		return "Paste delivery is uncertain; do not resend automatically"
	case "submission_uncertain":
		return "Submission shortcut delivery is uncertain; do not resend automatically"
	case "submission_interrupted":
		return "Prompt interrupted before native receipt confirmation; do not resend automatically"
	case "confirmation_timeout":
		return "Native submission was not confirmed; do not resend automatically"
	case "abort_uncertain":
		return "Abort delivery is uncertain; do not repeat automatically"
	case "abort_timeout":
		return "Abort settlement was not confirmed; do not repeat automatically"
	case "queued_cancelled":
		return "Queued command cancelled before delivery"
	case "runtime_stopped":
		return "Runtime stopped before command confirmation"
	case "queued_follow_up":
		return "Command queued until the current turn settles"
	case "queued_startup":
		return "Command queued until Claude is ready"
	case "native_confirmed":
		return "Native prompt receipt or abort settlement confirmed"
	case "command_submitted":
		return "Command submitted; awaiting native confirmation"
	case "delivery_failed":
		return "Terminal delivery failed; inspect the runtime"
	case ReasonSettingApplied:
		return "Runtime setting changed"
	case ReasonSettingRejected:
		return "Runtime refused a setting change"
	}
	return ""
}

// Reason codes for the outcome of a set command. A setting change is neither
// agent work nor a runtime failure, so it leaves agent status unchanged.
const (
	ReasonSettingApplied  = "setting_applied"
	ReasonSettingRejected = "setting_rejected"
)

// SettingOutcome reports whether an event settles a set command.
func SettingOutcome(reasonCode string) bool {
	return reasonCode == ReasonSettingApplied || reasonCode == ReasonSettingRejected
}

func ValidReasonCode(code string) bool { return code == "" || RuntimeReasonText(code) != "" }
