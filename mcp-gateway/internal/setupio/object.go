package setupio

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
)

func ConfigObject(raw []byte) (map[string]json.RawMessage, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("configuration object unavailable")
	}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, errors.New("configuration key unavailable")
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, errors.New("ambiguous configuration key")
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, errors.New("configuration value unavailable")
		}
		fields[key] = value
	}
	if _, err := d.Token(); err != nil || d.Decode(new(any)) != io.EOF {
		return nil, errors.New("configuration object incomplete")
	}
	return fields, nil
}
