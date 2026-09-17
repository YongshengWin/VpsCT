package safehttp

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

// CheckJSONBudget bounds decoded object amplification, not just wire bytes.
// Token scanning retains no object tree. Large collections/deep nesting must
// be rejected before Unmarshal allocates the complete configuration/state.
func CheckJSONBudget(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	depth := 0
	for tokens := 0; ; tokens++ {
		token, err := d.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if tokens >= 128<<10 {
			return errors.New("JSON value count exceeds memory budget")
		}
		switch v := token.(type) {
		case json.Delim:
			if v == '{' || v == '[' {
				depth++
			} else {
				depth--
			}
			if depth > 32 {
				return errors.New("JSON nesting exceeds memory budget")
			}
		case string:
			if len(v) > 64<<10 {
				return errors.New("JSON string exceeds memory budget")
			}
		}
	}
}
