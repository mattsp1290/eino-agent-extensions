package workspaceinstructions

import "strings"

const (
	sectionOpen     = `<workspace_instructions version="` + renderVersion + `">`
	sectionClose    = `</workspace_instructions>`
	fileClose       = `</file>`
	truncatedMarker = `[truncated: file exceeds the configured limit]`
	omittedMarker   = `[omitted: remaining instruction files exceed the configured section limit]`
)

func render(files []instructionFile, limits Limits) string {
	if len(files) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.Grow(min(limits.MaxSectionBytes, len(sectionOpen)+limits.MaxFileBytes+envelopeOverhead))
	builder.WriteString(sectionOpen)
	builder.WriteByte('\n')

	for index, file := range files {
		block := renderFile(file)
		needed := len(block) + len(sectionClose)
		if index < len(files)-1 {
			needed += len(omittedMarker) + 1
		}
		if builder.Len()+needed > limits.MaxSectionBytes {
			builder.WriteString(omittedMarker)
			builder.WriteByte('\n')
			break
		}
		builder.WriteString(block)
	}
	builder.WriteString(sectionClose)
	return builder.String()
}

func renderFile(file instructionFile) string {
	var builder strings.Builder
	builder.WriteString(`<file path="`)
	builder.WriteString(escapeAttribute(file.display))
	builder.WriteByte('"')
	if file.truncated {
		builder.WriteString(` truncated="true"`)
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
	return builder.String()
}

func escapeAttribute(value string) string {
	replacer := strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;")
	return replacer.Replace(value)
}
