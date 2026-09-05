package websearch

import (
	"encoding/json"
	"math"
	"net/url"
	"strings"
	"unicode/utf8"
)

func boundSources(records []Source, limits Limits) Result {
	count := len(records)
	if count > limits.MaxResults {
		count = limits.MaxResults
	}
	bounded := make([]Source, 0, count)
	for index := 0; index < count; index++ {
		record := records[index]
		if !validSourceURL(record.URL, limits.MaxURLBytes) {
			continue
		}
		bounded = append(bounded, Source{
			Title:   utf8Prefix(record.Title, limits.MaxTitleBytes),
			URL:     record.URL,
			Snippet: utf8Prefix(record.Snippet, limits.MaxSnippetBytes),
		})
	}
	return Result{Results: bounded}
}

func utf8Prefix(value string, maximum int) string {
	if len(value) > maximum {
		value = value[:maximum]
	}
	if utf8.ValidString(value) {
		return value
	}
	return strings.ToValidUTF8(value, "")
}

func validSourceURL(value string, maximum int) bool {
	if len(value) > maximum || !utf8.ValidString(value) {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return false
	}
	return parsed.Scheme == "http" || parsed.Scheme == "https"
}

func worstCaseResultBytes(limits Limits) (int64, error) {
	empty, err := json.Marshal(Result{Results: []Source{}})
	if err != nil {
		return 0, configError("result-retention")
	}
	one, err := json.Marshal(Result{Results: []Source{{}}})
	if err != nil || len(one) < len(empty) {
		return 0, configError("result-retention")
	}
	fieldBytes, ok := checkedAdd(int64(limits.MaxTitleBytes), int64(limits.MaxURLBytes))
	if !ok {
		return 0, configError("result-retention")
	}
	fieldBytes, ok = checkedAdd(fieldBytes, int64(limits.MaxSnippetBytes))
	if !ok {
		return 0, configError("result-retention")
	}
	escapedBytes, ok := checkedMul(fieldBytes, 6)
	if !ok {
		return 0, configError("result-retention")
	}
	perRecord, ok := checkedAdd(int64(len(one)-len(empty)), escapedBytes)
	if !ok {
		return 0, configError("result-retention")
	}
	recordsBytes, ok := checkedMul(perRecord, int64(limits.MaxResults))
	if !ok {
		return 0, configError("result-retention")
	}
	commas := int64(limits.MaxResults - 1)
	total, ok := checkedAdd(int64(len(empty)), recordsBytes)
	if !ok {
		return 0, configError("result-retention")
	}
	total, ok = checkedAdd(total, commas)
	if !ok || total <= 0 {
		return 0, configError("result-retention")
	}
	return total, nil
}

func checkedAdd(left, right int64) (int64, bool) {
	if left < 0 || right < 0 || left > math.MaxInt64-right {
		return 0, false
	}
	return left + right, true
}

func checkedMul(left, right int64) (int64, bool) {
	if left < 0 || right < 0 || (left != 0 && right > math.MaxInt64/left) {
		return 0, false
	}
	return left * right, true
}
