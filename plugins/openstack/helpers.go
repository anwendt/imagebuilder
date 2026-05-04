package openstack

import (
	"regexp"
	"strings"
)

var invalidOpenStackName = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func sanitizeOpenStackName(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "image"
	}
	value = invalidOpenStackName.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-._")
	if value == "" {
		return "image"
	}
	if len(value) > 63 {
		return strings.Trim(value[:63], "-._")
	}
	return value
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
