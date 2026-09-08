package commandguard

import "errors"

var errInternal = errors.New("command policy failed: internal")

func configError(class string) error {
	return errors.New("command policy configuration invalid: " + class)
}

type outcome uint8

const (
	abstain outcome = iota
	ruleMatch
	invalidCommand
	unanalysable
	analysisLimit
	capacity
)

func (o outcome) message() string {
	switch o {
	case ruleMatch:
		return "command policy denied: rule-match"
	case invalidCommand:
		return "command policy denied: invalid-command"
	case unanalysable:
		return "command policy denied: unanalysable-command"
	case analysisLimit:
		return "command policy denied: analysis-limit"
	case capacity:
		return "command policy denied: capacity"
	default:
		return ""
	}
}
