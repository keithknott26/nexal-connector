package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// CheckJSONObject rejects ambiguous duplicate members (including the
// case-insensitive aliases accepted by encoding/json) at every nesting level.
// Callers must still decode against their own schema and validate field values.
// Errors never include input bytes, which may contain credentials.
func CheckJSONObject(b []byte) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 64 {
			return errors.New("JSON nesting limit exceeded")
		}
		token, err := d.Token()
		if err != nil {
			return errors.New("invalid JSON")
		}
		delim, compound := token.(json.Delim)
		if depth == 0 && (!compound || delim != '{') {
			return errors.New("JSON object required")
		}
		if !compound {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return errors.New("invalid JSON object")
				}
				name, ok := key.(string)
				// Upper after lower also collapses Unicode aliases such as
				// long-s and Kelvin-sign accepted by encoding/json's folding.
				folded := strings.ToUpper(strings.ToLower(name))
				if !ok || seen[folded] {
					return errors.New("ambiguous JSON member")
				}
				seen[folded] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("invalid JSON delimiter")
		}
		if _, err := d.Token(); err != nil {
			return errors.New("invalid JSON closing delimiter")
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}
