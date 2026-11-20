package antigravity

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	protocol "github.com/regb/workitem/agentdriver/protocol"
)

// reasoningLevels are agy's effort levels; each model's session lists the
// thinking levels it accepts, and the driver maps onto the nearest one.
var reasoningLevels = []string{"low", "medium", "high", "xhigh", "max"}

var modelLine = regexp.MustCompile(`^[-*•\s]*([A-Za-z0-9][A-Za-z0-9._:/-]*)\s*(?:[-–:(]\s*(.*?)\)?)?\s*$`)

// ModelCatalog lists the models the signed-in account can use, from
// `agy models`, which starts no session.
func ModelCatalog(ctx context.Context, c Config) (protocol.ModelCatalog, error) {
	catalog := protocol.ModelCatalog{ReasoningLevels: reasoningLevels, Models: []protocol.ModelOption{}}
	executable, err := exec.LookPath(c.Executable)
	if err != nil {
		return catalog, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "models")
	cmd.Env = launchEnvironment(nil, "")
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err != nil || strings.Contains(text, "sign in") {
		return catalog, fmt.Errorf("agy models: %s", strings.TrimSpace(firstLine(text, err)))
	}
	return parseModels(text, catalog), nil
}

// parseModels reads `agy models`: one model per line, its ID and name
// separated by a tab, skipping status lines. agy lists a model once per
// thinking level, as in gemini-4-neon-high "Gemini 4 Neon (High)"; a
// session sets the model and the level apart, so the catalog lists the
// model once with its levels. A model listed at one level only keeps its ID.
func parseModels(text string, catalog protocol.ModelCatalog) protocol.ModelCatalog {
	type entry struct{ id, name string }
	var entries []entry
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasSuffix(line, "...") || strings.HasSuffix(line, ":") {
			continue
		}
		id, name, _ := strings.Cut(line, "\t")
		if m := modelLine.FindStringSubmatch(line); !strings.Contains(line, "\t") && m != nil {
			id, name = m[1], m[2]
		}
		if id = strings.TrimSpace(id); id != "" && !strings.ContainsAny(id, " ") {
			entries = append(entries, entry{id, strings.TrimSpace(name)})
		}
	}
	type group struct {
		label  string
		levels []string
		ids    []string
	}
	groups, order := map[string]*group{}, []string{}
	for _, e := range entries {
		base, level, label := splitLevel(e.id, e.name)
		g := groups[base]
		if g == nil {
			g = &group{label: label}
			groups[base] = g
			order = append(order, base)
		}
		g.ids = append(g.ids, e.id)
		if level != "" {
			g.levels = append(g.levels, level)
		}
	}
	for _, base := range order {
		g := groups[base]
		if len(g.ids) == 1 {
			name := ""
			for _, e := range entries {
				if e.id == g.ids[0] {
					name = e.name
				}
			}
			catalog.Models = append(catalog.Models, protocol.ModelOption{Value: g.ids[0], Label: name})
			continue
		}
		catalog.Models = append(catalog.Models, protocol.ModelOption{Value: base, Label: g.label, Reasoning: true, Levels: orderLevels(g.levels)})
	}
	return catalog
}

var levelSuffix = regexp.MustCompile(`^(.+)-(low|medium|high|xhigh|max)$`)
var levelLabel = regexp.MustCompile(`^(.+?)\s*\((Low|Medium|High|XHigh|Max|Extra)\)$`)

// splitLevel splits a model ID with a thinking level suffix into the model
// and the level, with the model's name without its level.
func splitLevel(id, name string) (string, string, string) {
	m := levelSuffix.FindStringSubmatch(id)
	if m == nil {
		return id, "", name
	}
	label := name
	if l := levelLabel.FindStringSubmatch(name); l != nil {
		label = l[1]
	}
	return m[1], m[2], label
}

// orderLevels sorts levels from shallow to deep.
func orderLevels(levels []string) []string {
	out := []string{}
	for _, level := range reasoningLadder {
		for _, l := range levels {
			if l == level {
				out = append(out, l)
				break
			}
		}
	}
	return out
}

func firstLine(text string, err error) string {
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasSuffix(line, "...") {
			return line
		}
	}
	if err != nil {
		return err.Error()
	}
	return "no models listed"
}
