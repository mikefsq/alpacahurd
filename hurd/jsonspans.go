package hurd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// keySpan is one top-level key of a device file and the byte range of its value. Offsets index
// the original JSONC text as well as editorJSON's output, which blanks comments in place.
type keySpan struct {
	key        string
	start, end int
}

// topLevelSpans lists the top-level keys of a JSON object and where each value lies.
func topLevelSpans(clean []byte) ([]keySpan, error) {
	dec := json.NewDecoder(bytes.NewReader(clean))
	tok, err := dec.Token()
	if err != nil || tok != json.Delim('{') {
		return nil, fmt.Errorf("configuration must be an object")
	}
	var out []keySpan
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, _ := tok.(string)
		start := int(dec.InputOffset())
		for start < len(clean) && strings.ContainsRune(": \t\r\n", rune(clean[start])) {
			start++
		}
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return nil, err
		}
		out = append(out, keySpan{key, start, int(dec.InputOffset())})
	}
	return out, nil
}

// patchDeviceText sets top-level keys, replacing every active occurrence (keys match without
// regard to case) and inserting missing keys at the top, preserving comments. It parses the text
// once for all keys.
func patchDeviceText(text string, values map[string]json.RawMessage) (string, error) {
	keys := make([]string, 0, len(values))
	for k, v := range values {
		if !json.Valid(v) {
			return "", fmt.Errorf("invalid value for %s", k)
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	clean, err := editorJSON(text)
	if err != nil {
		return "", err
	}
	spans, err := topLevelSpans(clean)
	if err != nil {
		return "", err
	}

	type edit struct {
		start, end int
		value      string
	}
	var edits []edit
	present := map[string]bool{}
	for _, s := range spans {
		for _, k := range keys {
			if strings.EqualFold(s.key, k) {
				edits = append(edits, edit{s.start, s.end, string(values[k])})
				present[k] = true
			}
		}
	}
	// Replace from the end so earlier offsets stay valid.
	sort.Slice(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	for _, e := range edits {
		text = text[:e.start] + e.value + text[e.end:]
	}

	var inserts []string
	for _, k := range keys {
		if present[k] {
			continue
		}
		name, _ := json.Marshal(k)
		inserts = append(inserts, "\n  "+string(name)+": "+string(values[k]))
	}
	if len(inserts) > 0 {
		start := bytes.IndexByte(clean, '{') + 1
		block := strings.Join(inserts, ",")
		if len(spans) > 0 {
			block += ","
		}
		text = text[:start] + block + text[start:]
	}
	return text, nil
}
