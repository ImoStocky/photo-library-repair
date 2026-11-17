// Package filter implements firewall-style image drop/accept rules for the
// materialized library (apply gate + plr filter), so junk does not stay in
// --target.
package filter

import (
	"bytes"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Action values for rules and the ruleset default.
const (
	ActionAccept   = "accept"
	ActionDrop     = "drop"
	ActionTag      = "tag"
	ActionClassify = "classify"
)

// DefaultRuleName is the reserved directory name for default drops.
const DefaultRuleName = "_default"

var ruleNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// Ruleset is a compiled, read-only filter configuration.
type Ruleset struct {
	Default     string // accept | drop
	Model       string // Kronk vision model source for classify (optional)
	Mmproj      string // optional vision projector GGUF path
	Rules       []compiledRule
	NeedsEXIF   bool
	HasClassify bool
}

type compiledRule struct {
	Name       string
	Action     string
	Tag        string
	Prompt     string            // classify only
	Categories map[string]string // label → description; classify only
	Labels     []string          // stable category keys for prompting
	Match      Matcher
	Line       int
}

// raw document shapes for KnownFields decoding.
type rawDoc struct {
	Version int       `yaml:"version"`
	Default string    `yaml:"default"`
	Model   string    `yaml:"model"`  // optional; CLI --model overrides
	Mmproj  string    `yaml:"mmproj"` // optional; CLI --mmproj overrides
	Rules   []rawRule `yaml:"rules"`
}

type rawRule struct {
	Name     string       `yaml:"name"`
	Match    Match        `yaml:"match"`
	Action   string       `yaml:"action"`
	Tag      string       `yaml:"tag"`
	Classify *rawClassify `yaml:"classify"`
}

type rawClassify struct {
	Model      string            `yaml:"model"`  // optional; same as top-level model
	Mmproj     string            `yaml:"mmproj"` // optional; same as top-level mmproj
	Prompt     string            `yaml:"prompt"`
	Categories map[string]string `yaml:"categories"`
}

// Match is a recursive boolean expression over image conditions.
type Match struct {
	All   []Match
	Any   []Match
	Not   *Match
	Conds map[string]yaml.Node
	Line  int
}

// UnmarshalYAML splits all/any/not from plain condition keys.
func (m *Match) UnmarshalYAML(value *yaml.Node) error {
	if value == nil || value.Kind == 0 || (value.Kind == yaml.ScalarNode && value.Tag == "!!null") {
		line := 0
		if value != nil {
			line = value.Line
		}
		*m = Match{Line: line}
		return nil
	}
	if value.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: match must be a mapping", value.Line)
	}
	out := Match{
		Conds: make(map[string]yaml.Node),
		Line:  value.Line,
	}
	for i := 0; i < len(value.Content); i += 2 {
		keyNode := value.Content[i]
		valNode := value.Content[i+1]
		switch keyNode.Value {
		case "all":
			var kids []Match
			if err := valNode.Decode(&kids); err != nil {
				return fmt.Errorf("line %d: all: %w", valNode.Line, err)
			}
			out.All = kids
		case "any":
			var kids []Match
			if err := valNode.Decode(&kids); err != nil {
				return fmt.Errorf("line %d: any: %w", valNode.Line, err)
			}
			out.Any = kids
		case "not":
			var kid Match
			if err := valNode.Decode(&kid); err != nil {
				return fmt.Errorf("line %d: not: %w", valNode.Line, err)
			}
			out.Not = &kid
		default:
			out.Conds[keyNode.Value] = *valNode
		}
	}
	*m = out
	return nil
}

// Load reads and compiles a filter-rules YAML file.
func Load(path string) (*Ruleset, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	var doc rawDoc
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("filter rules: %w", err)
	}
	return compileDoc(doc)
}

// LoadBytes compiles rules from YAML bytes (tests).
func LoadBytes(data []byte) (*Ruleset, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var doc rawDoc
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("filter rules: %w", err)
	}
	return compileDoc(doc)
}

func compileDoc(doc rawDoc) (*Ruleset, error) {
	if doc.Version != 1 {
		return nil, fmt.Errorf("filter rules: version must be 1 (got %d)", doc.Version)
	}
	def := doc.Default
	if def == "" {
		def = ActionAccept
	}
	if def != ActionAccept && def != ActionDrop {
		return nil, fmt.Errorf("filter rules: default must be accept or drop (got %q)", def)
	}

	rs := &Ruleset{
		Default: def,
		Model:   strings.TrimSpace(doc.Model),
		Mmproj:  strings.TrimSpace(doc.Mmproj),
		Rules:   make([]compiledRule, 0, len(doc.Rules)),
	}
	seen := map[string]bool{}
	for i, r := range doc.Rules {
		line := max(r.Match.Line, 0)
		if r.Name == "" {
			return nil, fmt.Errorf("filter rules: rule #%d: name is required", i+1)
		}
		if r.Name == DefaultRuleName {
			return nil, fmt.Errorf("filter rules: rule %q: name %q is reserved", r.Name, DefaultRuleName)
		}
		if !ruleNameRE.MatchString(r.Name) {
			return nil, fmt.Errorf("filter rules: rule %q: name must match %s", r.Name, ruleNameRE.String())
		}
		if seen[r.Name] {
			return nil, fmt.Errorf("filter rules: duplicate rule name %q", r.Name)
		}
		seen[r.Name] = true

		action := r.Action
		if action == "" {
			return nil, fmt.Errorf("filter rules: rule %q: action is required", r.Name)
		}
		cr := compiledRule{Name: r.Name, Action: action, Tag: r.Tag, Line: line}
		switch action {
		case ActionAccept, ActionDrop:
			if r.Tag != "" {
				return nil, fmt.Errorf("filter rules: rule %q: tag is only valid with action tag", r.Name)
			}
			if r.Classify != nil {
				return nil, fmt.Errorf("filter rules: rule %q: classify is only valid with action classify", r.Name)
			}
		case ActionTag:
			if r.Tag == "" {
				return nil, fmt.Errorf("filter rules: rule %q: tag action requires tag", r.Name)
			}
			if r.Classify != nil {
				return nil, fmt.Errorf("filter rules: rule %q: classify is only valid with action classify", r.Name)
			}
		case ActionClassify:
			if r.Tag != "" {
				return nil, fmt.Errorf("filter rules: rule %q: tag is only valid with action tag", r.Name)
			}
			if r.Classify == nil {
				return nil, fmt.Errorf("filter rules: rule %q: classify action requires classify block", r.Name)
			}
			if strings.TrimSpace(r.Classify.Prompt) == "" {
				return nil, fmt.Errorf("filter rules: rule %q: classify.prompt is required", r.Name)
			}
			if len(r.Classify.Categories) == 0 {
				return nil, fmt.Errorf("filter rules: rule %q: classify.categories must be non-empty", r.Name)
			}
			cr.Prompt = r.Classify.Prompt
			cr.Categories = make(map[string]string, len(r.Classify.Categories))
			cr.Labels = make([]string, 0, len(r.Classify.Categories))
			for label, desc := range r.Classify.Categories {
				if !ruleNameRE.MatchString(label) {
					return nil, fmt.Errorf("filter rules: rule %q: category %q: name must match %s", r.Name, label, ruleNameRE.String())
				}
				if label == DefaultRuleName {
					return nil, fmt.Errorf("filter rules: rule %q: category %q is reserved", r.Name, label)
				}
				cr.Categories[label] = desc
				cr.Labels = append(cr.Labels, label)
			}
			slices.Sort(cr.Labels)
			if err := mergeVisionConfig(rs, r.Name, r.Classify.Model, r.Classify.Mmproj); err != nil {
				return nil, err
			}
			rs.HasClassify = true
		default:
			return nil, fmt.Errorf("filter rules: rule %q: unknown action %q", r.Name, action)
		}

		matcher, needsEXIF, err := compileMatch(r.Match, r.Name)
		if err != nil {
			return nil, err
		}
		if needsEXIF {
			rs.NeedsEXIF = true
		}
		cr.Match = matcher
		rs.Rules = append(rs.Rules, cr)
	}
	return rs, nil
}

// mergeVisionConfig folds a classify rule's model/mmproj into the ruleset.
// Top-level YAML values win when already set; conflicting values error.
func mergeVisionConfig(rs *Ruleset, rule, model, mmproj string) error {
	model = strings.TrimSpace(model)
	mmproj = strings.TrimSpace(mmproj)
	if model != "" {
		if rs.Model != "" && rs.Model != model {
			return fmt.Errorf("filter rules: rule %q: classify.model %q conflicts with %q", rule, model, rs.Model)
		}
		if rs.Model == "" {
			rs.Model = model
		}
	}
	if mmproj != "" {
		if rs.Mmproj != "" && rs.Mmproj != mmproj {
			return fmt.Errorf("filter rules: rule %q: classify.mmproj %q conflicts with %q", rule, mmproj, rs.Mmproj)
		}
		if rs.Mmproj == "" {
			rs.Mmproj = mmproj
		}
	}
	return nil
}

// ResolveVisionModel picks the Kronk vision model and optional mmproj path.
// CLI values override YAML; fallback is used when neither sets model.
func ResolveVisionModel(cliModel, cliMmproj string, rs *Ruleset, fallback string) (model, mmproj string) {
	model = strings.TrimSpace(cliModel)
	if model == "" && rs != nil {
		model = strings.TrimSpace(rs.Model)
	}
	if model == "" {
		model = fallback
	}
	mmproj = strings.TrimSpace(cliMmproj)
	if mmproj == "" && rs != nil {
		mmproj = strings.TrimSpace(rs.Mmproj)
	}
	return model, mmproj
}

// CategoryDirs returns every classify label used as a folder name under the library.
func (rs *Ruleset) CategoryDirs() map[string]bool {
	if rs == nil {
		return nil
	}
	out := map[string]bool{}
	for _, r := range rs.Rules {
		for label := range r.Categories {
			out[label] = true
		}
	}
	return out
}
