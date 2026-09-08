package commandguard

import "strings"

// opaqueVariableTarget rejects shell-owned evaluation state. Some numeric
// variables evaluate assignments as arithmetic; prompts and startup variables
// can expand or execute values later. Apply this to POSIX too: sh may be Bash.
// BASH_* is reserved conservatively as a family, including BASH_ENV and future
// Bash-owned targets. Ordinary variables remain subject to the host's inherited
// environment/attribute contract; this is not runtime variable introspection.
func opaqueVariableTarget(name string) bool {
	if strings.HasPrefix(name, "BASH_") {
		return true
	}
	switch name {
	case "OPTIND", "RANDOM", "SRANDOM", "SECONDS", "HISTCMD", "MAILCHECK":
		return true
	case "PS0", "PS1", "PS2", "PS3", "PS4", "PROMPT_COMMAND", "ENV":
		return true
	}
	return false
}
