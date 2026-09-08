package commandguard

import "strings"

func (a *analysis) command(words []word, depth, wrappers int) outcome {
	if a.ctx.Err() != nil {
		return invalidCommand
	}
	if len(words) == 0 {
		return unanalysable
	}
	executable := words[0]
	if !executable.known || executable.text == "" || strings.HasSuffix(executable.text, "/") {
		return unanalysable
	}
	name := executable.text[strings.LastIndexByte(executable.text, '/')+1:]
	args := words[1:]
	for _, r := range a.policy.rules {
		if a.ctx.Err() != nil {
			return invalidCommand
		}
		if r.Executable != name {
			continue
		}
		match := true
		for i, want := range r.ArgPrefix {
			if i >= len(args) {
				match = false
				break
			}
			if !args[i].known {
				return unanalysable
			}
			if args[i].text != want {
				match = false
				break
			}
		}
		if match {
			return ruleMatch
		}
	}
	if o := builtin(name, args); o != abstain {
		return o
	}
	return a.wrapper(name, args, depth, wrappers)
}

func builtin(name string, args []word) outcome {
	switch name {
	case "eval", ".", "source", "alias", "unalias", "builtin", "enable", "trap", "fc", "history", "bind", "complete", "compgen", "read", "unset", "getopts", "mapfile", "readarray", "declare", "typeset", "local", "export", "readonly", "let":
		return unanalysable
	case "printf":
		if len(args) > 0 && (!args[0].known || (strings.HasPrefix(args[0].text, "-") && args[0].text != "--")) {
			return unanalysable
		}
	case "test", "[":
		for _, arg := range args {
			if !arg.known || arg.text == "-v" || arg.text == "-R" {
				return unanalysable
			}
		}
		if name == "[" && (len(args) == 0 || args[len(args)-1].text != "]") {
			return unanalysable
		}
	}
	return abstain
}
