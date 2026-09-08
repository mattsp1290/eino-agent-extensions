package commandguard

import (
	"bytes"
	"encoding/json"
	"io"
)

// input walks tokens rather than materializing arbitrary unknown JSON values.
// The raw byte bound applies before the decoder can allocate string buffers.
func (a *analysis) input(raw []byte, field string) (string, outcome) {
	l := a.policy.limits
	if len(raw) > l.MaxRawInputBytes {
		return "", analysisLimit
	}
	if !validText(string(raw)) {
		return "", invalidCommand
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	nodes := 0
	take := func() (json.Token, outcome) {
		if a.ctx.Err() != nil {
			return nil, invalidCommand
		}
		if nodes >= l.MaxJSONNodes {
			return nil, analysisLimit
		}
		nodes++
		tok, err := d.Token()
		if err != nil {
			return nil, invalidCommand
		}
		if s, ok := tok.(string); ok && !validText(s) {
			return nil, invalidCommand
		}
		return tok, abstain
	}
	command := ""
	found := false
	var value func(int, bool) outcome
	value = func(depth int, root bool) outcome {
		if depth > l.MaxJSONDepth {
			return analysisLimit
		}
		tok, o := take()
		if o != abstain {
			return o
		}
		delim, container := tok.(json.Delim)
		if root && (!container || delim != '{') {
			return invalidCommand
		}
		if !container {
			return abstain
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, o := take()
				if o != abstain {
					return o
				}
				s, ok := key.(string)
				if !ok || seen[s] {
					return invalidCommand
				}
				seen[s] = true
				if root && s == field {
					if depth >= l.MaxJSONDepth {
						return analysisLimit
					}
					v, o := take()
					if o != abstain {
						return o
					}
					command, ok = v.(string)
					if !ok {
						return invalidCommand
					}
					if len(command) > l.MaxCommandBytes {
						return analysisLimit
					}
					found = true
				} else if o := value(depth+1, false); o != abstain {
					return o
				}
			}
		case '[':
			for d.More() {
				if o := value(depth+1, false); o != abstain {
					return o
				}
			}
		default:
			return invalidCommand
		}
		end, err := d.Token()
		if err != nil {
			return invalidCommand
		}
		if (delim == '{' && end != json.Delim('}')) || (delim == '[' && end != json.Delim(']')) {
			return invalidCommand
		}
		return abstain
	}
	if o := value(1, true); o != abstain {
		return "", o
	}
	if _, err := d.Token(); err != io.EOF || !found {
		return "", invalidCommand
	}
	return command, abstain
}
