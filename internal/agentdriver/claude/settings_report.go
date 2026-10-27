package claude

import (
	protocol "github.com/regb/workitem/agentdriver/protocol"
)

// settingsReport tells wi the model and reasoning level Claude runs with,
// once each is known and again whenever it changes.
type settingsReport struct {
	emit   func(protocol.RuntimeEvent)
	values map[string]string
	// aliases map model values such as "opus" to the model they name.
	aliases map[string]string
}

// replies reports the model and effort of the transcript's latest reply,
// which Claude records but its stream does not carry.
func (r *settingsReport) replies(feed *transcriptFeed) {
	r.report(protocol.SettingModel, feed.model)
	r.report(protocol.SettingReasoning, feed.effort)
}

func (r *settingsReport) report(name, value string) {
	if name == protocol.SettingModel && r.aliases[value] != "" {
		value = r.aliases[value]
	}
	// Claude marks a reply it made up itself, such as an error, with a
	// placeholder model.
	if value == "" || value == "<synthetic>" || r.values[name] == value {
		return
	}
	settings := map[string]string{name: value}
	if protocol.ValidSettingValues(settings) != nil {
		return
	}
	if r.values == nil {
		r.values = map[string]string{}
	}
	r.values[name] = value
	r.emit(protocol.RuntimeEvent{Type: protocol.EventSettingsChanged, Settings: settings})
}
