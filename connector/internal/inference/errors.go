package inference

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Error is a failure with a stable machine code and a concrete fix. The CLI
// prints Code/Message/Fix in the install "error" event.
type Error struct {
	Code    string
	Message string
	Fix     string
}

func (e *Error) Error() string { return e.Message }

func errf(code, fix, format string, a ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, a...), Fix: fix}
}

// AsError returns err as an *Error, wrapping unknown errors as io_error.
func AsError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Code: "io_error", Message: err.Error(), Fix: "check disk space and permissions on the neXal config folder, then try again"}
}

// scanStrict walks one JSON value and rejects duplicate object keys.
func scanStrict(dec *json.Decoder) error {
	t, err := dec.Token()
	if err != nil {
		return err
	}
	d, ok := t.(json.Delim)
	if !ok {
		return nil
	}
	switch d {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return err
			}
			key, _ := kt.(string)
			if seen[key] {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			seen[key] = true
			if err := scanStrict(dec); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := scanStrict(dec); err != nil {
				return err
			}
		}
	}
	_, err = dec.Token()
	return err
}

// CheckNoDuplicateKeys rejects duplicate object keys at any depth and any data
// after the first JSON value (Go's decoder accepts both silently; the runtime's
// strict_json does not).
func CheckNoDuplicateKeys(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := scanStrict(dec); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("unexpected data after the JSON document")
	}
	return nil
}

// DecodeStrict decodes data into v: no duplicate keys, no unknown fields, no
// trailing data.
func DecodeStrict(data []byte, v any) error {
	if err := CheckNoDuplicateKeys(data); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// writeFileAtomic writes data to path (temp file in the same directory, fsync,
// rename) with the given mode. The parent is created 0700. An existing target
// must be a regular file; symlinks are never followed or replaced.
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := ensurePrivateDir(dir); err != nil {
		return err
	}
	if st, err := os.Lstat(path); err == nil && !st.Mode().IsRegular() {
		return fmt.Errorf("%s exists and is not a regular file", filepath.Base(path))
	}
	f, err := os.CreateTemp(dir, ".nexal-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(mode); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// ensurePrivateDir creates dir (and parents) and makes it a real directory,
// not a symlink, with mode 0700.
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	st, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !st.IsDir() {
		return fmt.Errorf("%s is not a directory (symlinks are refused)", dir)
	}
	if st.Mode().Perm() != 0o700 {
		return os.Chmod(dir, 0o700)
	}
	return nil
}
