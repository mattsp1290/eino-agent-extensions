package workspaceinstructions

import (
	"bytes"
	"context"
	"strings"
)

const (
	sectionOpen     = `<workspace_instructions version="` + renderVersion + `">`
	sectionClose    = `</workspace_instructions>`
	fileOpenPrefix  = `<file path="`
	truncatedAttr   = ` truncated="true"`
	fileClose       = `</file>`
	truncatedMarker = `[truncated: file exceeds the configured limit]`
	omittedMarker   = `[omitted: remaining instruction files exceed the configured section limit]`
	lineBreakBytes  = 1
)

type sectionSink struct {
	buffer      bytes.Buffer
	checkpoints []int
	limit       int
	started     bool
	omitted     bool
}

func newSectionSink(limit int) *sectionSink {
	return &sectionSink{limit: limit}
}

func (sink *sectionSink) add(file instructionFile) bool {
	if sink.omitted {
		return false
	}
	if !sink.started {
		sink.started = true
		sink.buffer.WriteString(sectionOpen)
		sink.buffer.WriteByte('\n')
	}
	if sink.buffer.Len()+renderedFileSize(file.display, len(file.content), file.truncated)+len(sectionClose) <= sink.limit {
		sink.checkpoints = append(sink.checkpoints, sink.buffer.Len())
		appendFile(&sink.buffer, file)
		return true
	}

	// Omission is only known after a file does not fit. Roll back whole prior
	// files as needed so the marker and closing tag remain within the limit.
	sink.omitted = true
	markerBudget := sink.limit - len(omittedMarker) - lineBreakBytes - len(sectionClose)
	for sink.buffer.Len() > markerBudget && len(sink.checkpoints) > 0 {
		last := len(sink.checkpoints) - 1
		sink.buffer.Truncate(sink.checkpoints[last])
		sink.checkpoints = sink.checkpoints[:last]
	}
	return false
}

func (sink *sectionSink) finish() string {
	if !sink.started {
		return ""
	}
	if sink.omitted {
		sink.buffer.WriteString(omittedMarker)
		sink.buffer.WriteByte('\n')
	}
	sink.buffer.WriteString(sectionClose)
	return sink.buffer.String()
}

func renderWorkspaceWithReader(ctx context.Context, workspace canonicalWorkspace, fileNames []string, limits Limits, reader candidateReader) (string, error) {
	sink := newSectionSink(limits.MaxSectionBytes)
	err := walkInstructionFiles(ctx, workspace, fileNames, limits, reader, sink.add)
	if err != nil {
		return "", err
	}
	return sink.finish(), nil
}

func appendFile(builder *bytes.Buffer, file instructionFile) {
	builder.WriteString(fileOpenPrefix)
	builder.WriteString(escapeAttribute(file.display))
	builder.WriteByte('"')
	if file.truncated {
		builder.WriteString(truncatedAttr)
	}
	builder.WriteString(">\n")
	builder.WriteString(file.content)
	builder.WriteByte('\n')
	if file.truncated {
		builder.WriteString(truncatedMarker)
		builder.WriteByte('\n')
	}
	builder.WriteString(fileClose)
	builder.WriteByte('\n')
}

func renderedFileSize(display string, contentBytes int, truncated bool) int {
	size := len(fileOpenPrefix) + len(escapeAttribute(display)) + 1 + 1 +
		lineBreakBytes + contentBytes + lineBreakBytes + len(fileClose) + lineBreakBytes
	if truncated {
		size += len(truncatedAttr) + len(truncatedMarker) + lineBreakBytes
	}
	return size
}

func minimumSectionBytes(fileNames []string, limits Limits) int {
	maximumDisplay := ""
	for _, fileName := range fileNames {
		display := displayPath(limits.MaxChainDepth, 0, fileName)
		if len(escapeAttribute(display)) > len(escapeAttribute(maximumDisplay)) {
			maximumDisplay = display
		}
	}
	fileSize := renderedFileSize(maximumDisplay, limits.MaxFileBytes, true)
	return len(sectionOpen) + lineBreakBytes + fileSize + len(omittedMarker) + lineBreakBytes + len(sectionClose)
}

func escapeAttribute(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(value)
}
