// Package redact removes connection secrets before diagnostics cross a trust boundary.
package redact

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"
)

const Replacement = "[REDACTED]"

var (
	// Connection strings may contain nested SQL, shell, or JSON quoting. Discard
	// the rest of the diagnostic line instead of guessing where nesting ends.
	connection    = regexp.MustCompile(`(?im)((?:primary_conninfo|conninfo)\s*["']?\s*[:=]\s*).*$`)
	assignment    = regexp.MustCompile(`(?i)((?:[a-z0-9_]*password|[a-z0-9_]*passwd|[a-z0-9_]*secret|[a-z0-9_]*token|passfile|api[_-]?key)\s*["']?\s*[:=]\s*)(?:'(?:[^'\\]|\\.|'')*'|"(?:[^"\\]|\\.)*"|[^\s,;}&]+)`)
	bearer        = regexp.MustCompile(`(?i)(\b(?:bearer|basic)\s+)[a-z0-9._~+/=-]+`)
	uriPassword   = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^\s/@:]+:)[^\s/@]*@`)
	mysqlPassword = regexp.MustCompile(`(\s-p)[^\s]+`)
	sqlPassword   = regexp.MustCompile("(?i)((?:\\bPASSWORD|\\bIDENTIFIED(?:\\s+WITH\\s+[`a-z0-9_]+)?\\s+(?:BY|AS))\\s+)(?:'(?:[^'\\\\]|\\\\.|'')*'|\"(?:[^\"\\\\]|\\\\.)*\")")
	scramVerifier = regexp.MustCompile(`SCRAM-SHA-256\$[0-9]+:[A-Za-z0-9+/=]+\$[A-Za-z0-9+/=]+:[A-Za-z0-9+/=]+`)
	privateKey    = regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z]+ )?PRIVATE KEY-----.*?-----END (?:[A-Z]+ )?PRIVATE KEY-----`)
	sensitiveKey  = regexp.MustCompile(`(?i)(?:^|[_-])(?:password|passwd|secret|token|passfile|conninfo|api[_-]?key)(?:$|[_-])`)
)

func Text(value string, secrets ...string) string {
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		encoded, _ := json.Marshal(secret)
		for _, representation := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret), strings.Trim(string(encoded), `"`)} {
			if representation != "" {
				value = strings.ReplaceAll(value, representation, Replacement)
			}
		}
	}
	value = connection.ReplaceAllString(value, "${1}"+Replacement)
	value = assignment.ReplaceAllString(value, "${1}"+Replacement)
	value = bearer.ReplaceAllString(value, "${1}"+Replacement)
	value = sqlPassword.ReplaceAllString(value, "${1}"+Replacement)
	value = scramVerifier.ReplaceAllString(value, Replacement)
	value = privateKey.ReplaceAllString(value, Replacement)
	value = uriPassword.ReplaceAllString(value, "${1}"+Replacement+"@")
	return mysqlPassword.ReplaceAllString(value, "${1}"+Replacement)
}

// JSON sanitizes diagnostic payloads without changing number precision or
// authorization booleans. Do not use it for intentional credential issuance.
func JSON(data []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value interface{}
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("invalid trailing diagnostic JSON")
	}
	var clean func(interface{}) interface{}
	clean = func(value interface{}) interface{} {
		switch v := value.(type) {
		case string:
			return Text(v)
		case []interface{}:
			for i := range v {
				v[i] = clean(v[i])
			}
			return v
		case map[string]interface{}:
			for key, item := range v {
				// Observation tokens are public topology CAS fingerprints, not credentials.
				_, boolean := item.(bool)
				if item != nil && !boolean && key != "observation_token" && sensitiveKey.MatchString(key) {
					v[key] = Replacement
				} else {
					v[key] = clean(item)
				}
			}
			return v
		default:
			return v
		}
	}
	return json.Marshal(clean(value))
}

func Bounded(value string, limit int, secrets ...string) string {
	value = Text(value, secrets...)
	if limit > 0 && len(value) > limit {
		value = value[:limit]
	}
	return value
}
