package commandguard

import (
	"regexp"
	"strings"
)

var durationShape = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?[smhd]?$`)
var signalShape = regexp.MustCompile(`^([0-9]+|[A-Za-z][A-Za-z0-9]*)$`)

func variableName(s string) bool {
	if s == "" {
		return false
	}
	for i, c := range []byte(s) {
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
func assignment(s string) bool { key, _, ok := strings.Cut(s, "="); return ok && variableName(key) }
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
)

func validOperand(s string, kind optionValue) bool {
	if s == "" {
		return false
	}
	switch kind {
	case variable:
		return variableName(s)
	case duration:
		return durationShape.MatchString(s)
	case signalName:
		return signal(s)
	}
	return true
}

// wrapper implements an enumerated grammar, never general option equivalence.
func (a *analysis) wrapper(name string, args []word, depth, wrappers int) outcome {
	switch name {
	case "env", "command", "exec", "sudo", "timeout", "sh", "bash":
	default:
		return abstain
	}
	if wrappers >= a.policy.limits.MaxWrapperDepth || depth >= a.policy.limits.MaxASTDepth {
		return analysisLimit
	}
	wrappers++
	if name == "sh" || name == "bash" {
		return a.shell(name, args, depth+1, wrappers)
	}
	i := 0
	ended := false
	for i < len(args) {
		if a.ctx.Err() != nil {
			return invalidCommand
		}
		if !args[i].known {
			return unanalysable
		}
		s := args[i].text
		if s == "--" {
			i++
			ended = true
			break
		}
		if !strings.HasPrefix(s, "-") {
			break
		}
		flag := false
		operand := false
		kind := nonempty
		long := ""
		switch name {
		case "env":
			switch s {
			case "-i", "--ignore-environment":
				flag = true
			case "-u":
				operand = true
				kind = variable
			case "-C":
				operand = true
			}
			for _, entry := range []struct {
				prefix string
				kind   optionValue
			}{{"--unset=", variable}, {"--chdir=", nonempty}} {
				if strings.HasPrefix(s, entry.prefix) {
					long = entry.prefix
					kind = entry.kind
					break
				}
			}
		case "command":
			flag = s == "-p" && i == 0
		case "exec":
		case "sudo":
			switch s {
			case "-n", "-E", "-H":
				flag = true
			case "-u", "-g", "-D":
				operand = true
			}
			for _, prefix := range []string{"--user=", "--group=", "--chdir="} {
				if strings.HasPrefix(s, prefix) {
					long = prefix
					break
				}
			}
		case "timeout":
			switch s {
			case "--foreground", "--preserve-status", "-v", "--verbose":
				flag = true
			case "-s":
				operand = true
				kind = signalName
			case "-k":
				operand = true
				kind = duration
			}
			if strings.HasPrefix(s, "--signal=") {
				long = "--signal="
				kind = signalName
			} else if strings.HasPrefix(s, "--kill-after=") {
				long = "--kill-after="
				kind = duration
			}
		}
		switch {
		case flag:
			i++
		case operand:
			if i+1 >= len(args) || !args[i+1].known || !validOperand(args[i+1].text, kind) {
				return unanalysable
			}
			i += 2
		case long != "":
			if !validOperand(strings.TrimPrefix(s, long), kind) {
				return unanalysable
			}
			i++
		default:
			return unanalysable
		}
		if name == "command" { // only one -p, then optional --
			if i < len(args) && args[i].known && args[i].text == "--" {
				i++
				ended = true
			}
			break
		}
	}
	if name == "command" && !ended && i < len(args) && (!args[i].known || strings.HasPrefix(args[i].text, "-")) {
		return unanalysable
	}
	if name == "env" || name == "sudo" {
		for i < len(args) {
			if !args[i].known {
				return unanalysable
			}
			if !assignment(args[i].text) {
				if strings.Contains(args[i].text, "=") {
					return unanalysable
				}
				break
			}
			i++
		}
	}
	// An option-shaped sudo selector after environment operands is outside
	// the bounded command grammar.
	if name == "sudo" && i < len(args) && strings.HasPrefix(args[i].text, "-") {
		return unanalysable
	}
	if name == "timeout" {
		if i >= len(args) || !args[i].known || !durationShape.MatchString(args[i].text) {
			return unanalysable
		}
		i++
	}
	if i == len(args) {
		if name == "env" {
			return abstain
		}
		return unanalysable
	}
	// An option-shaped command after an explicit -- is an ordinary executable.
	// Without --, unsupported flags have already failed above.
	return a.command(args[i:], depth+1, wrappers)
}

func (a *analysis) shell(name string, args []word, depth, wrappers int) outcome {
	i := 0
	for i < len(args) && args[i].known && (args[i].text == "-e" || args[i].text == "-u" || args[i].text == "-x") {
		i++
	}
	if i >= len(args) || !args[i].known || (args[i].text != "-c" && args[i].text != "-lc") {
		return unanalysable
	}
	i++
	if i >= len(args) || !args[i].known {
		return unanalysable
	}
	script := args[i].text
	if strings.HasPrefix(script, "-") || strings.HasPrefix(script, "+") {
		return unanalysable
	}
	if i+1 < len(args) && !args[i+1].known {
		return unanalysable
	}
	dialect := DialectPOSIX
	if name == "bash" {
		dialect = DialectBash
	}
	return a.script(script, dialect, depth, wrappers)
}
