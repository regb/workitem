package driverprotocol

import "testing"

func TestSetCommandCarriesExactlyOneBoundedSetting(t *testing.T) {
	valid := ControlCommand{Type: CommandSet, Setting: &Setting{Name: "model", Value: "openai-codex/gpt-6-sol"}}
	if err := valid.ValidateContent(); err != nil {
		t.Fatal(err)
	}
	for name, command := range map[string]ControlCommand{
		"missing setting":       {Type: CommandSet},
		"message on set":        {Type: CommandSet, Message: "hi", Setting: &Setting{Name: "model", Value: "x"}},
		"setting on prompt":     {Type: CommandPrompt, Message: "hi", Setting: &Setting{Name: "model", Value: "x"}},
		"uppercase name":        {Type: CommandSet, Setting: &Setting{Name: "Model", Value: "x"}},
		"empty value":           {Type: CommandSet, Setting: &Setting{Name: "model"}},
		"multi-line value":      {Type: CommandSet, Setting: &Setting{Name: "model", Value: "a\nb"}},
		"flag-like name spaces": {Type: CommandSet, Setting: &Setting{Name: "my model", Value: "x"}},
	} {
		if command.ValidateContent() == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}
