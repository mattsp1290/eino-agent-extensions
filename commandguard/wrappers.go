package commandguard

import "strings"

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
	remaining, result := a.wrapperArguments(name, args)
	if result != abstain {
		return result
	}
	if len(remaining) == 0 {
		if name == "env" {
			return abstain
		}
		return unanalysable
	}
	return a.command(remaining, depth+1, wrappers)
}

func (a *analysis) wrapperArguments(name string, args []word) ([]word, outcome) {
	if name == "command" || name == "exec" {
		return builtinCommandPrefix(name, args)
	}
	args, result := a.scanOptions(args, wrapperOptions[name])
	if result != abstain {
		return nil, result
	}
	switch name {
	case "env", "sudo":
		args, result = assignmentOperands(args)
		if result != abstain {
			return nil, result
		}
		// sudo can reinterpret option-shaped selectors after assignments.
		if name == "sudo" && len(args) > 0 && strings.HasPrefix(args[0].text, "-") {
			return nil, unanalysable
		}
	case "timeout":
		if len(args) == 0 || !args[0].known || !durationShape.MatchString(args[0].text) {
			return nil, unanalysable
		}
		args = args[1:]
	}
	return args, abstain
}

// command accepts one -p and then optional --; exec accepts only optional --.
func builtinCommandPrefix(name string, args []word) ([]word, outcome) {
	if name == "command" && len(args) > 0 && args[0].known && args[0].text == "-p" {
		args = args[1:]
	}
	if len(args) > 0 {
		if args[0].known && args[0].text == "--" {
			return args[1:], abstain
		}
		if !args[0].known || strings.HasPrefix(args[0].text, "-") {
			return nil, unanalysable
		}
	}
	return args, abstain
}

func assignmentOperands(args []word) ([]word, outcome) {
	for len(args) > 0 {
		if !args[0].known {
			return nil, unanalysable
		}
		key, _, assignment := strings.Cut(args[0].text, "=")
		if !assignment {
			break
		}
		if !variableName(key) || opaqueVariableTarget(key) {
			return nil, unanalysable
		}
		args = args[1:]
	}
	return args, abstain
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
