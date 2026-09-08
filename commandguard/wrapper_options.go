package commandguard

import (
	"regexp"
	"strings"
)

var durationShape = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?[smhd]?$`)
var signalShape = regexp.MustCompile(`^([0-9]+|[A-Za-z][A-Za-z0-9]*)$`)

func signal(s string) bool {
	if !signalShape.MatchString(s) {
		return false
	}
	if s[0] >= '0' && s[0] <= '9' {
		return strings.Trim(s, "0") != ""
	}
	return true
}

type optionValue uint8

const (
	nonempty optionValue = iota
	variable
	duration
	signalName
	flagOption
)

func validOperand(s string, kind optionValue) bool {
	if s == "" {
		return false
	}
	switch kind {
	case nonempty:
		return true
	case variable:
		return variableName(s)
	case duration:
		return durationShape.MatchString(s)
	case signalName:
		return signal(s)
	}
	return false
}

type optionSpec struct {
	spelling string // A trailing '=' requires an attached operand.
	kind     optionValue
}

var wrapperOptions = map[string][]optionSpec{
	"env": {
		{"-i", flagOption}, {"--ignore-environment", flagOption},
		{"-u", variable}, {"--unset=", variable},
		{"-C", nonempty}, {"--chdir=", nonempty},
	},
	"sudo": {
		{"-n", flagOption}, {"-E", flagOption}, {"-H", flagOption},
		{"-u", nonempty}, {"--user=", nonempty},
		{"-g", nonempty}, {"--group=", nonempty},
		{"-D", nonempty}, {"--chdir=", nonempty},
	},
	"timeout": {
		{"--foreground", flagOption}, {"--preserve-status", flagOption},
		{"-v", flagOption}, {"--verbose", flagOption},
		{"-s", signalName}, {"--signal=", signalName},
		{"-k", duration}, {"--kill-after=", duration},
	},
}

func (a *analysis) scanOptions(args []word, specs []optionSpec) ([]word, outcome) {
	for len(args) > 0 {
		if a.ctx.Err() != nil {
			return nil, invalidCommand
		}
		if !args[0].known {
			return nil, unanalysable
		}
		if args[0].text == "--" {
			return args[1:], abstain
		}
		if !strings.HasPrefix(args[0].text, "-") {
			break
		}
		consumed := consumeOption(args, specs)
		if consumed == 0 {
			return nil, unanalysable
		}
		args = args[consumed:]
	}
	return args, abstain
}

// Zero means unsupported or invalid. This scanner never expands option aliases.
func consumeOption(args []word, specs []optionSpec) int {
	for _, spec := range specs {
		if strings.HasSuffix(spec.spelling, "=") {
			if value, found := strings.CutPrefix(args[0].text, spec.spelling); found && validOperand(value, spec.kind) {
				return 1
			}
			continue
		}
		if args[0].text != spec.spelling {
			continue
		}
		if spec.kind == flagOption {
			return 1
		}
		if len(args) > 1 && args[1].known && validOperand(args[1].text, spec.kind) {
			return 2
		}
		return 0
	}
	return 0
}
