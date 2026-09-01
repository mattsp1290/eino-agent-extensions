package toolresultredactor

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/mattsp1290/eino-agent/runtime"
)

var structuredPlaceholder = json.RawMessage(`"[REDACTED]"`)

type scanState uint8

const (
	scanUnchanged scanState = iota
	scanChanged
	scanUnsafe
	scanCanceled
)

type compiledPolicy struct {
	limits   Limits
	rules    []compiledRule
	excluded map[string]struct{}
}

func compilePolicy(options Options) (canonicalOptions, *compiledPolicy, error) {
	canonical, err := canonicalize(options)
	if err != nil {
		return canonicalOptions{}, nil, err
	}
	policy, err := compileCanonicalPolicy(canonical)
	if err != nil {
		return canonicalOptions{}, nil, err
	}
	return canonical, policy, nil
}

func compileCanonicalPolicy(canonical canonicalOptions) (*compiledPolicy, error) {
	rules, err := compileRules(canonical.additionalPatterns)
	if err != nil {
		return nil, err
	}
	excluded := make(map[string]struct{}, len(canonical.excludedTools))
	for _, name := range canonical.excludedTools {
		excluded[name] = struct{}{}
	}
	return &compiledPolicy{limits: canonical.limits, rules: rules, excluded: excluded}, nil
}

func (p *compiledPolicy) redact(ctx context.Context, input runtime.ToolResult) (output runtime.ToolResult) {
	output = input
	defer func() {
		if recover() != nil {
			output = fullyPlaceholderized(input)
		}
	}()
	return p.redactChecked(ctx, input)
}

func (p *compiledPolicy) redactChecked(ctx context.Context, input runtime.ToolResult) runtime.ToolResult {
	if ctx.Err() != nil {
		return fullyPlaceholderized(input)
	}
	output := runtime.ToolResult{}
	var state scanState
	output.Output, state = p.scanScalar(ctx, input.Output)
	if state == scanCanceled {
		return fullyPlaceholderized(input)
	}

	output.Structured, state = p.scanStructured(ctx, input.Structured)
	if state == scanCanceled {
		return fullyPlaceholderized(input)
	}

	output.Metadata, state = p.scanMetadata(ctx, input.Metadata)
	if state == scanCanceled {
		return fullyPlaceholderized(input)
	}

	output.Attachments, state = p.scanAttachments(ctx, input.Attachments)
	if state == scanCanceled {
		return fullyPlaceholderized(input)
	}
	if ctx.Err() != nil {
		return fullyPlaceholderized(input)
	}
	return output
}

func (p *compiledPolicy) scanScalar(ctx context.Context, value string) (string, scanState) {
	if ctx.Err() != nil {
		return Placeholder, scanCanceled
	}
	if value == Placeholder {
		return value, scanUnchanged
	}
	if len(value) > p.limits.MaxFieldBytes || !utf8.ValidString(value) {
		return Placeholder, scanUnsafe
	}
	ranges := make([]byteRange, 0)
	matches := 0
	for _, rule := range p.rules {
		if ctx.Err() != nil {
			return Placeholder, scanCanceled
		}
		remaining := p.limits.MaxMatchesPerField - matches
		queryLimit := len(value)
		if remaining < len(value) {
			queryLimit = remaining + 1
		}
		found := rule.matcher.find(value, queryLimit)
		if len(found) > remaining {
			return Placeholder, scanUnsafe
		}
		matches += len(found)
		ranges = append(ranges, found...)
	}
	if ctx.Err() != nil {
		return Placeholder, scanCanceled
	}
	if len(ranges) == 0 {
		return value, scanUnchanged
	}
	ranges = mergeRanges(ranges)
	var result strings.Builder
	result.Grow(len(value))
	position := 0
	for _, span := range ranges {
		result.WriteString(value[position:span.start])
		result.WriteString(Placeholder)
		position = span.end
	}
	result.WriteString(value[position:])
	return result.String(), scanChanged
}

func (p *compiledPolicy) scanMetadata(ctx context.Context, input map[string]string) (map[string]string, scanState) {
	if input == nil {
		return nil, scanUnchanged
	}
	if len(input) > p.limits.MaxMetadataEntries {
		return metadataPlaceholder(), scanUnsafe
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		_, keyState := p.scanScalar(ctx, key)
		if keyState == scanCanceled {
			return metadataPlaceholder(), scanCanceled
		}
		if keyState != scanUnchanged {
			return metadataPlaceholder(), scanUnsafe
		}
		redacted, valueState := p.scanScalar(ctx, value)
		if valueState == scanCanceled {
			return metadataPlaceholder(), scanCanceled
		}
		output[key] = redacted
	}
	return output, scanUnchanged
}

func (p *compiledPolicy) scanAttachments(ctx context.Context, input []runtime.Attachment) ([]runtime.Attachment, scanState) {
	if input == nil {
		return nil, scanUnchanged
	}
	if len(input) > p.limits.MaxAttachments {
		return attachmentPlaceholder(), scanUnsafe
	}
	output := make([]runtime.Attachment, len(input))
	for index, attachment := range input {
		if ctx.Err() != nil {
			return attachmentPlaceholder(), scanCanceled
		}
		var state scanState
		output[index].ID, state = p.scanScalar(ctx, attachment.ID)
		if state == scanCanceled {
			return attachmentPlaceholder(), scanCanceled
		}
		output[index].MIMEType, state = p.scanScalar(ctx, attachment.MIMEType)
		if state == scanCanceled {
			return attachmentPlaceholder(), scanCanceled
		}
		output[index].Name, state = p.scanScalar(ctx, attachment.Name)
		if state == scanCanceled {
			return attachmentPlaceholder(), scanCanceled
		}
		output[index].URL, state = p.scanScalar(ctx, attachment.URL)
		if state == scanCanceled {
			return attachmentPlaceholder(), scanCanceled
		}
		output[index].Metadata, state = p.scanMetadata(ctx, attachment.Metadata)
		if state == scanCanceled {
			return attachmentPlaceholder(), scanCanceled
		}
	}
	return output, scanUnchanged
}

func fullyPlaceholderized(input runtime.ToolResult) runtime.ToolResult {
	output := runtime.ToolResult{Output: Placeholder}
	if input.Structured != nil {
		output.Structured = append(json.RawMessage(nil), structuredPlaceholder...)
	}
	if input.Metadata != nil {
		output.Metadata = metadataPlaceholder()
	}
	if input.Attachments != nil {
		output.Attachments = attachmentPlaceholder()
	}
	return output
}

func metadataPlaceholder() map[string]string {
	return map[string]string{"": Placeholder}
}

func attachmentPlaceholder() []runtime.Attachment {
	return []runtime.Attachment{{Name: Placeholder}}
}
