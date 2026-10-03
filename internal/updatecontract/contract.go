// Package updatecontract validates the mandatory rules compiled into every consumer.
package updatecontract

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
)

const SupportedVersion = 2

//go:embed contract.md
var Document string

//go:embed schema.json
var schema []byte

func Validate() error { return Parse(Document) }

// Parse accepts only the exact supported section 21 schema. Examples elsewhere
// in the document cannot substitute for the mandatory contract.
func Parse(source string) error {
	fail := func(reason string) error { return fmt.Errorf("CG_CONTRACT_UNAVAILABLE: %s", reason) }
	marker := "# 21. Machine-Readable Contract"
	if strings.Count(source, marker) != 1 {
		return fail("missing or duplicate section 21")
	}
	section := strings.SplitN(source, marker, 2)[1]
	if end := strings.Index(section, "\n# 22."); end >= 0 {
		section = section[:end]
	} else {
		return fail("missing section boundary")
	}
	if strings.Count(section, "```yaml\n") != 1 {
		return fail("missing or duplicate YAML block")
	}
	block := strings.SplitN(section, "```yaml\n", 2)[1]
	end := strings.Index(block, "```")
	if end < 0 {
		return fail("unterminated YAML block")
	}
	var expected map[string]string
	if json.Unmarshal(schema, &expected) != nil {
		return fail("invalid compiled schema")
	}
	values, groups := map[string]string{}, map[string]bool{}
	group := ""
	for _, line := range strings.Split(block[:end], "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return fail("invalid YAML field")
		}
		value = strings.TrimSpace(value)
		if !strings.HasPrefix(line, " ") {
			if value != "" || strings.TrimSpace(key) != key || groups[key] {
				return fail("invalid or duplicate group")
			}
			known := false
			for field := range expected {
				if strings.HasPrefix(field, key+".") {
					known = true
					break
				}
			}
			if !known {
				return fail("unknown group " + key)
			}
			groups[key], group = true, key
			continue
		}
		if group == "" || !strings.HasPrefix(key, "  ") || strings.HasPrefix(key, "   ") || strings.TrimSpace(key[2:]) != key[2:] {
			return fail("invalid nesting")
		}
		field := group + "." + key[2:]
		wanted, exists := expected[field]
		if !exists {
			return fail("unknown field " + field)
		}
		if _, duplicate := values[field]; duplicate {
			return fail("duplicate field " + field)
		}
		if value != wanted {
			return fail("unsupported value for " + field)
		}
		values[field] = value
	}
	if len(values) != len(expected) {
		return fail("missing required fields")
	}
	if values["contract.version"] != fmt.Sprint(SupportedVersion) {
		return fail("unsupported contract version")
	}
	return nil
}
