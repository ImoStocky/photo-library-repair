package filter

// Decision is the terminal result of evaluating a ruleset against one image.
type Decision struct {
	Action string // accept | drop
	Rule   string // matching rule name, or DefaultRuleName for default
	Tags   []string
}

// ExplainStep records one rule evaluation for --explain.
type ExplainStep struct {
	Rule    string
	Matched bool
	Action  string
	Detail  string
}

// Evaluate applies first-match-wins rules; tag is non-terminal.
func (rs *Ruleset) Evaluate(info *ImageInfo) Decision {
	if rs == nil {
		return Decision{Action: ActionAccept, Rule: DefaultRuleName}
	}
	var tags []string
	for _, r := range rs.Rules {
		if !r.Match(info) {
			continue
		}
		switch r.Action {
		case ActionTag:
			tags = append(tags, r.Tag)
			continue
		case ActionClassify:
			// Model I/O lives in FilterDir's classify phase; sync Evaluate continues.
			continue
		case ActionAccept, ActionDrop:
			return Decision{Action: r.Action, Rule: r.Name, Tags: tags}
		}
	}
	return Decision{Action: rs.Default, Rule: DefaultRuleName, Tags: tags}
}

// Explain evaluates like Evaluate but returns every rule's match result.
func (rs *Ruleset) Explain(info *ImageInfo) (Decision, []ExplainStep) {
	if rs == nil {
		d := Decision{Action: ActionAccept, Rule: DefaultRuleName}
		return d, nil
	}
	var tags []string
	var steps []ExplainStep
	for _, r := range rs.Rules {
		matched := r.Match(info)
		step := ExplainStep{Rule: r.Name, Matched: matched, Action: r.Action}
		if matched {
			switch r.Action {
			case ActionTag:
				tags = append(tags, r.Tag)
				step.Detail = "tag=" + r.Tag + " (continue)"
				steps = append(steps, step)
				continue
			case ActionClassify:
				step.Detail = "classify (deferred to model phase)"
				steps = append(steps, step)
				continue
			case ActionAccept, ActionDrop:
				step.Detail = "terminal"
				steps = append(steps, step)
				return Decision{Action: r.Action, Rule: r.Name, Tags: tags}, steps
			}
		}
		step.Detail = "no match"
		steps = append(steps, step)
	}
	d := Decision{Action: rs.Default, Rule: DefaultRuleName, Tags: tags}
	steps = append(steps, ExplainStep{
		Rule:    DefaultRuleName,
		Matched: true,
		Action:  rs.Default,
		Detail:  "default",
	})
	return d, steps
}
