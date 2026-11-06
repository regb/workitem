package cli

import (
	"fmt"
	"github.com/regb/workitem/internal/model"
	"strings"
)

func effectiveListLabelRules(configRules []string, env map[string]string, cliRules []string) (map[string]bool, error) {
	rules := map[string]bool{}
	if err := applyListLabelRules(rules, configRules, "config"); err != nil {
		return nil, err
	}
	if raw, ok := env["WI_LIST_LABELS"]; ok {
		if err := applyListLabelRules(rules, []string{raw}, "WI_LIST_LABELS"); err != nil {
			return nil, err
		}
	}
	if err := applyListLabelRules(rules, cliRules, "--label"); err != nil {
		return nil, err
	}
	return rules, nil
}

func applyListLabelRules(effective map[string]bool, values []string, source string) error {
	for _, value := range values {
		for _, token := range strings.Split(value, ",") {
			token = strings.TrimSpace(token)
			if token == "" {
				continue
			}
			if token == "!" {
				clear(effective)
				continue
			}
			include := true
			if token[0] == '+' || token[0] == '-' {
				include = token[0] != '-'
				token = strings.TrimSpace(token[1:])
			}
			label, err := model.NormalizeLabel(token)
			if err != nil {
				return fmt.Errorf("invalid %s label rule %q: %w", source, token, err)
			}
			effective[label] = include
		}
	}
	return nil
}
