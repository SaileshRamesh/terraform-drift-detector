// Package normalize holds value canonicalizers shared by resource handlers.
package normalize

import (
	"encoding/json"
	"sort"
	"strings"
)

// listKeys are IAM policy elements that accept either a scalar or a list.
var listKeys = map[string]bool{
	"Statement": true, "Action": true, "NotAction": true,
	"Resource": true, "NotResource": true,
	"AWS": true, "Service": true, "Federated": true, "CanonicalUser": true,
}

// PolicyJSON returns a canonical form of an IAM policy document, so that
// documents differing only in whitespace, key order, list order, or
// scalar-vs-single-element-list compare equal. Invalid JSON is returned as is.
func PolicyJSON(doc string) string {
	if strings.TrimSpace(doc) == "" {
		return ""
	}
	var v any
	if err := json.Unmarshal([]byte(doc), &v); err != nil {
		return doc
	}
	b, err := json.Marshal(canonical(v, ""))
	if err != nil {
		return doc
	}
	return string(b)
}

func canonical(v any, key string) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = canonical(val, k)
		}
		if key == "Statement" {
			return []any{out}
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = canonical(val, "")
		}
		sortStrings(out)
		return out
	case string:
		if listKeys[key] && key != "Statement" {
			return []any{t}
		}
	}
	return v
}

// sortStrings sorts list in place when every element is a string.
func sortStrings(list []any) {
	for _, v := range list {
		if _, ok := v.(string); !ok {
			return
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].(string) < list[j].(string) })
}
