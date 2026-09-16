package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
)

// validateJSON rejects duplicate (including case variants), deeply nested and
// trailing values before decoding into a security-sensitive structure.
func validateJSON(b []byte) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 64 {
			return errors.New("JSON 嵌套过深")
		}
		tok, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			keys := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return e
				}
				s, ok := k.(string)
				if !ok {
					return errors.New("invalid key")
				}
				s = strings.ToLower(s)
				if keys[s] {
					return errors.New("JSON 字段重复")
				}
				keys[s] = true
				if e = value(depth + 1); e != nil {
					return e
				}
			}
		case '[':
			for d.More() {
				if e := value(depth + 1); e != nil {
					return e
				}
			}
		default:
			return errors.New("invalid delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("JSON 含尾随内容")
	}
	return nil
}
