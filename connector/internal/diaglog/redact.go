package diaglog

import (
	"bytes"
	"io"
	"os"
	"regexp"
	"strings"
)

// Redact removes anything token-like from text that is about to leave this Mac
// in a diagnostics bundle. The agent already never logs secrets; this is the
// second line of defence for log tails and runtime output, so it errs towards
// over-redacting.
func Redact(s string) string {
	s = privateKeyBlock.ReplaceAllString(s, "[redacted private key]")
	s = bearer.ReplaceAllString(s, "${1}[redacted]")
	s = urlUserinfo.ReplaceAllString(s, "://[redacted]@")
	s = secretField.ReplaceAllStringFunc(s, func(m string) string {
		sub := secretField.FindStringSubmatch(m)
		if len(sub) != 3 || harmlessValue(sub[2]) {
			return m
		}
		return sub[1] + `"[redacted]"`
	})
	s = prefixedToken.ReplaceAllString(s, "[redacted]")
	s = macAddress.ReplaceAllString(s, "[mac]")
	s = longRun.ReplaceAllStringFunc(s, func(m string) string {
		if strings.ContainsAny(m, "0123456789") && strings.IndexFunc(m, isLetter) >= 0 {
			return "[redacted]"
		}
		return m
	})
	return s
}

var (
	privateKeyBlock = regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z0-9 ]*PRIVATE KEY-----`)
	bearer          = regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`)
	urlUserinfo     = regexp.MustCompile(`://[^/\s:@"]+:[^/\s@"]+@`)
	// A key whose name says it holds a secret, in JSON ("key":"v") or k=v form.
	secretField = regexp.MustCompile(`(?i)("?[A-Za-z_-]*(?:token|secret|password|passwd|passphrase|setup_?key|api_?key|private_?key|credential|psk)[A-Za-z_-]*"?\s*[:=]\s*)("(?:[^"\\]|\\.)*"|[^\s,}&]+)`)
	// Product and common vendor token shapes.
	prefixedToken = regexp.MustCompile(`\b(?:enr|nxl|nxt|tok|sk|pk|hk|adm|ghp|gho|xox[abp])_[A-Za-z0-9_-]{8,}`)
	macAddress    = regexp.MustCompile(`(?i)\b[0-9a-f]{2}(?::[0-9a-f]{2}){5}\b`)
	// A long unbroken run of key-alphabet characters mixing letters and digits.
	longRun = regexp.MustCompile(`[A-Za-z0-9+/_=-]{32,}`)
)

func harmlessValue(v string) bool {
	v = strings.Trim(v, `"`)
	switch strings.ToLower(v) {
	case "", "true", "false", "null", "[redacted]":
		return true
	}
	return strings.Trim(v, "0123456789.") == ""
}

func isLetter(r rune) bool { return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') }

// Tail returns up to max bytes from the end of the file at path, starting at a
// line boundary. A missing file yields "".
func Tail(path string, max int64) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return ""
	}
	start := int64(0)
	if st.Size() > max {
		start = st.Size() - max
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return ""
	}
	b, err := io.ReadAll(io.LimitReader(f, max))
	if err != nil {
		return ""
	}
	if start > 0 {
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		}
	}
	return string(b)
}
