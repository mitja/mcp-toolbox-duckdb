// Copyright 2026 Mitja Martini
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package duckdbsql

import (
	"fmt"
	"regexp"
	"strings"
)

// DefaultAllowedStatementKinds is the set of leading SQL keywords accepted
// by the duckdb-sql tool when no policy override is given. It intentionally
// excludes FROM (not a valid statement opener) and any DDL/DML.
var DefaultAllowedStatementKinds = []string{
	"SELECT", "WITH", "DESCRIBE", "SHOW", "EXPLAIN", "PIVOT", "UNPIVOT", "VALUES", "TABLE",
}

// forbiddenSubstrings are keywords that, even when they appear as part of an
// otherwise-allowed statement, indicate an attempt to escape the read-only
// posture. The check is case-insensitive and word-boundary aware (matches a
// token, not a substring of an identifier).
//
// NOTE: this is defense in depth. The actual security boundary is the Quack
// server's read_only authorization callback. The agent never builds SQL —
// only bound values — so these patterns can only appear if the developer
// writes them into tools.yaml.
var forbiddenSubstrings = []string{
	"INSTALL", "LOAD", "ATTACH", "DETACH", "CREATE", "DROP", "ALTER",
	"INSERT", "UPDATE", "DELETE", "TRUNCATE", "MERGE", "COPY",
	"GRANT", "REVOKE", "CALL", "PRAGMA", "SET",
}

// identTokenRe matches one ASCII SQL identifier-like token.
var identTokenRe = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)

// ValidateStatement checks that statement is a single read-only SQL
// statement whose leading keyword is in allowedKinds and whose normalized
// form contains no forbidden tokens. It is meant to run once at tool
// config-load time (or at every invocation for the dev duckdb-execute-sql
// tool, which takes agent-supplied SQL). It is NOT a SQL sandbox.
//
// allowedKinds is uppercased internally; pass any case. extraForbidden
// extends the built-in forbiddenSubstrings list with operator-supplied
// patterns (typically from Source.Policy.ForbiddenPatterns); pass nil to
// use only the built-in deny list.
func ValidateStatement(statement string, allowedKinds, extraForbidden []string) error {
	if strings.TrimSpace(statement) == "" {
		return fmt.Errorf("statement is empty")
	}

	stripped, semicolonInMiddle, err := stripStringsAndComments(statement)
	if err != nil {
		return fmt.Errorf("statement parse error: %w", err)
	}
	if semicolonInMiddle {
		return fmt.Errorf("statement contains more than one SQL statement; multiple statements are not allowed")
	}

	leading := leadingKeyword(stripped)
	if leading == "" {
		return fmt.Errorf("statement has no leading SQL keyword")
	}
	allowed := false
	for _, k := range allowedKinds {
		if strings.EqualFold(leading, k) {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("statement leading keyword %q is not in the allowed set %v", leading, upperAll(allowedKinds))
	}

	upper := strings.ToUpper(stripped)
	for _, bad := range forbiddenSubstrings {
		if containsToken(upper, bad) {
			return fmt.Errorf("statement contains forbidden token %q", bad)
		}
	}
	for _, bad := range extraForbidden {
		needle := strings.ToUpper(strings.TrimSpace(bad))
		if needle == "" {
			continue
		}
		if containsToken(upper, needle) {
			return fmt.Errorf("statement contains forbidden token %q (from policy.forbidden_patterns)", bad)
		}
	}
	return nil
}

// stripStringsAndComments returns the input with string literals and comment
// bodies replaced by spaces. It also reports whether an unquoted, uncommented
// `;` appears anywhere except at the very end (after which only whitespace is
// allowed) — that flag drives the multi-statement rejection.
//
// Supported tokens:
//   - `'...'`     single-quoted strings, `''` is a literal quote
//   - `E'...'`    DuckDB escape strings, `\` is the escape character
//   - `"..."`     quoted identifiers, `""` is a literal double-quote
//   - `--...\n`   line comments
//   - `/*...*/`   block comments (not nested)
func stripStringsAndComments(s string) (stripped string, semicolonInMiddle bool, err error) {
	var b strings.Builder
	b.Grow(len(s))
	state := stateNormal
	i := 0
	for i < len(s) {
		c := s[i]
		switch state {
		case stateNormal:
			switch {
			case c == '\'':
				b.WriteByte(' ')
				state = stateSingleQuote
				i++
			case c == '"':
				b.WriteByte(' ')
				state = stateDoubleQuote
				i++
			case c == 'E' || c == 'e':
				// Look for E'...' escape-string syntax.
				if i+1 < len(s) && s[i+1] == '\'' {
					b.WriteByte(' ')
					b.WriteByte(' ')
					state = stateEscapeString
					i += 2
				} else {
					b.WriteByte(c)
					i++
				}
			case c == '-' && i+1 < len(s) && s[i+1] == '-':
				state = stateLineComment
				i += 2
			case c == '/' && i+1 < len(s) && s[i+1] == '*':
				state = stateBlockComment
				i += 2
			case c == ';':
				// A trailing `;` (only whitespace after it) is allowed.
				rest := strings.TrimSpace(s[i+1:])
				if rest != "" {
					semicolonInMiddle = true
				}
				b.WriteByte(' ')
				i++
			default:
				b.WriteByte(c)
				i++
			}
		case stateSingleQuote:
			if c == '\'' {
				if i+1 < len(s) && s[i+1] == '\'' { // doubled '' is an escaped quote
					b.WriteByte(' ')
					b.WriteByte(' ')
					i += 2
					continue
				}
				b.WriteByte(' ')
				state = stateNormal
				i++
				continue
			}
			b.WriteByte(' ')
			i++
		case stateEscapeString:
			if c == '\\' && i+1 < len(s) { // \X is a single escaped char
				b.WriteByte(' ')
				b.WriteByte(' ')
				i += 2
				continue
			}
			if c == '\'' {
				b.WriteByte(' ')
				state = stateNormal
				i++
				continue
			}
			b.WriteByte(' ')
			i++
		case stateDoubleQuote:
			if c == '"' {
				if i+1 < len(s) && s[i+1] == '"' { // doubled "" is an escaped quote
					b.WriteByte(' ')
					b.WriteByte(' ')
					i += 2
					continue
				}
				b.WriteByte(' ')
				state = stateNormal
				i++
				continue
			}
			b.WriteByte(' ')
			i++
		case stateLineComment:
			if c == '\n' {
				b.WriteByte('\n')
				state = stateNormal
			} else {
				b.WriteByte(' ')
			}
			i++
		case stateBlockComment:
			if c == '*' && i+1 < len(s) && s[i+1] == '/' {
				b.WriteByte(' ')
				b.WriteByte(' ')
				state = stateNormal
				i += 2
				continue
			}
			b.WriteByte(' ')
			i++
		}
	}
	switch state {
	case stateSingleQuote, stateEscapeString:
		return "", false, fmt.Errorf("unterminated single-quoted string")
	case stateDoubleQuote:
		return "", false, fmt.Errorf("unterminated double-quoted identifier")
	case stateBlockComment:
		return "", false, fmt.Errorf("unterminated block comment")
	}
	return b.String(), semicolonInMiddle, nil
}

// parser states for stripStringsAndComments.
const (
	stateNormal = iota
	stateSingleQuote
	stateEscapeString
	stateDoubleQuote
	stateLineComment
	stateBlockComment
)

// leadingKeyword returns the first identifier-like token in stripped SQL
// (with strings and comments already replaced by spaces), uppercased. Returns
// "" if no token is found.
func leadingKeyword(stripped string) string {
	match := identTokenRe.FindString(stripped)
	if match == "" {
		return ""
	}
	return strings.ToUpper(match)
}

// containsToken reports whether word (already uppercase) appears in upper as
// a whole token (delimited by non-identifier characters on both sides).
func containsToken(upper, word string) bool {
	idx := 0
	for {
		j := strings.Index(upper[idx:], word)
		if j < 0 {
			return false
		}
		start := idx + j
		end := start + len(word)
		if !isIdentByte(prevByte(upper, start)) && !isIdentByte(nextByte(upper, end)) {
			return true
		}
		idx = end
	}
}

func isIdentByte(b byte) bool {
	return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '_'
}

func prevByte(s string, i int) byte {
	if i <= 0 {
		return 0
	}
	return s[i-1]
}

func nextByte(s string, i int) byte {
	if i >= len(s) {
		return 0
	}
	return s[i]
}

func upperAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToUpper(s)
	}
	return out
}
