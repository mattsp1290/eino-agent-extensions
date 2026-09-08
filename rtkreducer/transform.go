package rtkreducer

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/mattsp1290/eino-agent/runtime"
)

type reduceFieldFunc func(context.Context, Filter, string) (string, bool)
type acquireReductionFunc func(context.Context) (reduceFieldFunc, func(), bool)

type transformer struct {
	limits   Limits
	bindings map[string]canonicalBinding
	acquire  acquireReductionFunc
}

func newTransformer(options canonicalOptions, acquire acquireReductionFunc) *transformer {
	bindings := make(map[string]canonicalBinding, len(options.bindings))
	for _, binding := range options.bindings {
		bindings[binding.toolName] = binding
	}
	return &transformer{limits: options.limits, bindings: bindings, acquire: acquire}
}

func (t *transformer) transform(ctx context.Context, input runtime.ToolResultTransform) (output runtime.ToolResultTransform, err error) {
	original := input
	output = original
	defer func() {
		if recover() != nil {
			output = original
			err = nil
		}
	}()

	binding, exists := t.bindings[input.ToolName]
	if !exists || ctx.Err() != nil || input.Result.Metadata["permission_status"] != "" {
		return original, nil
	}
	if binding.matchInput != nil && !t.matchesInput(input.Call.Input, binding.matchInput) {
		return original, nil
	}
	reduce, release, admitted := t.acquire(ctx)
	if !admitted || reduce == nil || release == nil {
		return original, nil
	}
	defer release()
	if ctx.Err() != nil || !boundedCombinedSize(len(input.Result.Output), len(input.Result.Structured), t.limits.MaxResultBytes) {
		return original, nil
	}

	switch binding.mode {
	case TextOnly:
		return t.transformText(ctx, original, binding, reduce), nil
	case JSONMirror:
		return t.transformJSON(ctx, original, binding, reduce), nil
	default:
		return original, nil
	}
}

func (t *transformer) matchesInput(raw json.RawMessage, match *canonicalInputMatch) bool {
	if len(raw) > t.limits.MaxInputBytes {
		return false
	}
	found, ok := scanObjectStrings(raw, map[string]struct{}{match.field: {}}, t.limits.MaxJSONDepth, t.limits.MaxJSONNodes)
	if !ok {
		return false
	}
	span, exists := found[match.field]
	if !exists {
		return false
	}
	index := sort.SearchStrings(match.equals, span.value)
	return index < len(match.equals) && match.equals[index] == span.value
}

func (t *transformer) transformText(ctx context.Context, original runtime.ToolResultTransform, binding canonicalBinding, reduce reduceFieldFunc) runtime.ToolResultTransform {
	if len(original.Result.Structured) != 0 || len(binding.fields) != 1 {
		return original
	}
	text := original.Result.Output
	if len(text) > t.limits.MaxFieldBytes || !utf8.ValidString(text) || strings.IndexByte(text, 0) >= 0 || text == "" {
		return original
	}
	reduced, ok := reduce(ctx, binding.fields[0].Filter, text)
	if !ok || reduced == text || reduced == "" || !utf8.ValidString(reduced) {
		return original
	}
	marked := reductionMarker(binding.fields[0].Filter, len(text)) + reduced
	if len(marked) >= len(text) || !boundedCombinedSize(len(marked), 0, t.limits.MaxResultBytes) {
		return original
	}
	result := original.Result
	result.Output = marked
	changed := original
	changed.Result = result
	return changed
}

type fieldReplacement struct {
	start int
	end   int
	value []byte
}

func (t *transformer) transformJSON(ctx context.Context, original runtime.ToolResultTransform, binding canonicalBinding, reduce reduceFieldFunc) runtime.ToolResultTransform {
	if len(original.Result.Structured) == 0 || len(original.Result.Output) != len(original.Result.Structured) || original.Result.Output != string(original.Result.Structured) {
		return original
	}
	raw := original.Result.Structured
	requested := make(map[string]struct{}, len(binding.fields))
	for _, field := range binding.fields {
		requested[field.Name] = struct{}{}
	}
	spans, ok := scanObjectStrings(raw, requested, t.limits.MaxJSONDepth, t.limits.MaxJSONNodes)
	if !ok || len(spans) != len(binding.fields) {
		return original
	}
	for _, field := range binding.fields {
		span := spans[field.Name]
		if len(span.value) > t.limits.MaxFieldBytes || !utf8.ValidString(span.value) || strings.IndexByte(span.value, 0) >= 0 {
			return original
		}
	}

	replacements := make([]fieldReplacement, 0, len(binding.fields))
	for _, field := range binding.fields {
		span := spans[field.Name]
		if span.value == "" {
			continue
		}
		reduced, success := reduce(ctx, field.Filter, span.value)
		if !success || reduced == "" || !utf8.ValidString(reduced) {
			return original
		}
		if reduced == span.value {
			continue
		}
		marked := reductionMarker(field.Filter, len(span.value)) + reduced
		if len(marked) >= len(span.value) {
			continue
		}
		encoded, marshalErr := json.Marshal(marked)
		if marshalErr != nil || len(encoded) >= span.end-span.start {
			continue
		}
		replacements = append(replacements, fieldReplacement{start: span.start, end: span.end, value: encoded})
	}
	if len(replacements) == 0 {
		return original
	}
	sort.Slice(replacements, func(i, j int) bool { return replacements[i].start < replacements[j].start })
	newLength := len(raw)
	for _, replacement := range replacements {
		delta := (replacement.end - replacement.start) - len(replacement.value)
		if delta <= 0 || delta > newLength {
			return original
		}
		newLength -= delta
	}
	if newLength >= len(raw) || !boundedCombinedSize(newLength, newLength, t.limits.MaxResultBytes) {
		return original
	}
	patched := make([]byte, 0, newLength)
	position := 0
	for _, replacement := range replacements {
		if replacement.start < position || replacement.end > len(raw) {
			return original
		}
		patched = append(patched, raw[position:replacement.start]...)
		patched = append(patched, replacement.value...)
		position = replacement.end
	}
	patched = append(patched, raw[position:]...)
	if len(patched) != newLength || !json.Valid(patched) {
		return original
	}

	result := original.Result
	result.Output = string(patched)
	result.Structured = append(json.RawMessage(nil), patched...)
	changed := original
	changed.Result = result
	return changed
}

func reductionMarker(filter Filter, originalBytes int) string {
	return "[Reduced by RTK " + string(filter) + "; original_bytes=" + strconv.Itoa(originalBytes) + "]\n"
}

func boundedCombinedSize(left, right, maximum int) bool {
	return left >= 0 && right >= 0 && maximum >= 0 && left <= maximum && right <= maximum-left
}
