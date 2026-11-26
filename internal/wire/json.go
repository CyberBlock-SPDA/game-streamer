package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"unicode/utf8"
)

// Object enforces the exact, case-sensitive, required non-null fields in an envelope.
func Object(raw []byte, names ...string) (map[string]json.RawMessage, error) {
	if !utf8.Valid(raw) {
		return nil, errors.New("invalid UTF-8")
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return nil, err
	}
	if len(obj) != len(names) {
		return nil, errors.New("unexpected fields")
	}
	for _, name := range names {
		if v := obj[name]; v == nil || string(v) == "null" {
			return nil, errors.New("missing field")
		}
	}
	return obj, nil
}

func Type(raw []byte) string {
	var h struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(raw, &h)
	return h.Type
}

func Strict(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
