package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
)

// SetValue replaces one top-level entry of an existing user.config.json with
// value, written in the indentation of the file. Everything else keeps its
// text. It refuses a file that lists the key twice.
func SetValue(path, key string, value any) error {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("read user configuration: %w", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read user configuration: %w", err)
	}
	var want map[string]any
	if err := json.Unmarshal(data, &want); err != nil {
		return fmt.Errorf("parse user configuration: %w", err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode %q: %w", key, err)
	}
	var plain any
	if err := json.Unmarshal(encoded, &plain); err != nil {
		return fmt.Errorf("encode %q: %w", key, err)
	}
	want[key] = plain

	updated, err := setValueText(data, key, value)
	if err != nil {
		return fmt.Errorf("cannot change %q in place in %s: %w", key, path, err)
	}
	var got map[string]any
	if json.Unmarshal(updated, &got) != nil || !reflect.DeepEqual(got, want) {
		return fmt.Errorf("cannot change %q in place in %s", key, path)
	}
	return replaceFile(path, updated, info.Mode().Perm())
}

// ChangeText replaces top-level values in user.config.json text, each in the
// indentation of the file, and parses the result like Load. It refuses a key
// the file lists twice, a value that is not one JSON value, and a result that
// fails the configuration checks.
func ChangeText(data []byte, values map[string]json.RawMessage) ([]byte, User, error) {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	updated := data
	for _, key := range keys {
		if !json.Valid(values[key]) {
			return nil, User{}, fmt.Errorf("%s: the value is not valid JSON", key)
		}
		var err error
		if updated, err = setValueText(updated, key, values[key]); err != nil {
			return nil, User{}, fmt.Errorf("cannot change %q: %w", key, err)
		}
	}
	user, err := Parse(updated)
	if err != nil {
		return nil, User{}, err
	}
	return updated, user, nil
}

// WriteText replaces the configuration file at path with data and keeps its
// permissions. It refuses a path that is a link.
func WriteText(path string, data []byte) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("read user configuration: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a plain file", path)
	}
	return replaceFile(path, data, info.Mode().Perm())
}

// setValueText swaps the text of the top-level value of key, or inserts the
// key as the first entry when the file has none.
func setValueText(data []byte, key string, value any) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("not a JSON object")
	}
	start, end := -1, -1
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		if tok != key {
			continue
		}
		if start >= 0 {
			return nil, fmt.Errorf("listed twice")
		}
		end = int(dec.InputOffset())
		start = end - len(raw)
	}

	unit := indentUnit(data)
	if start < 0 {
		open := bytes.IndexByte(data, '{')
		text, err := indentJSON(value, unit, unit)
		if err != nil {
			return nil, err
		}
		entry := "\n" + unit + fmt.Sprintf("%q: ", key) + string(text)
		if rest := bytes.TrimSpace(data[open+1:]); len(rest) > 0 && rest[0] != '}' {
			entry += ","
		}
		return append(append(append([]byte{}, data[:open+1]...), entry...), data[open+1:]...), nil
	}
	text, err := indentJSON(value, lineIndent(data, start), unit)
	if err != nil {
		return nil, err
	}
	return append(append(append([]byte{}, data[:start]...), text...), data[end:]...), nil
}

// indentJSON is json.MarshalIndent without escaping <, > and &, which add-on
// permissions such as <all_urls> use.
func indentJSON(value any, prefix, unit string) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent(prefix, unit)
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// lineIndent returns the leading white space of the line holding offset.
func lineIndent(data []byte, offset int) string {
	line := data[bytes.LastIndexByte(data[:offset], '\n')+1 : offset]
	return string(line[:len(line)-len(bytes.TrimLeft(line, " \t"))])
}

// indentUnit is the indentation of the first top-level entry, two spaces
// when the file is on one line.
func indentUnit(data []byte) string {
	open := bytes.IndexByte(data, '{')
	if open < 0 {
		return "  "
	}
	rest := data[open+1:]
	nl := bytes.IndexByte(rest, '\n')
	if nl < 0 {
		return "  "
	}
	line := rest[nl+1:]
	if unit := line[:len(line)-len(bytes.TrimLeft(line, " \t"))]; len(unit) > 0 {
		return string(unit)
	}
	return "  "
}
