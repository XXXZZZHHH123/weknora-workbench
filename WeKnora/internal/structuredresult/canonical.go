// Package structuredresult validates additive structured result contracts.
// A column dependency is deliberately not a claim of SQL cell lineage.
package structuredresult

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"sort"
	"strconv"
	"strings"
)

const HashAlgorithm = "sha256-typed-json-v1"

// TypedSHA256 hashes JSON values with explicit type and UTF-8 byte length.
// Numeric strings remain strings; JSON numbers use their normalized decimal
// value. Use json.Decoder.UseNumber when reading the original response, before
// any conversion to float64, to preserve large integers/decimal lexical values.
func TypedSHA256(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil || len(encoded) > 8*1024*1024 {
		return "", fmt.Errorf("structured result digest input invalid or over budget")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var normalized any
	if err = decoder.Decode(&normalized); err != nil {
		return "", err
	}
	digest := sha256.New()
	if err = writeTyped(digest, normalized, 0); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func frame(digest hash.Hash, tag, value string) {
	_, _ = digest.Write([]byte(tag + strconv.Itoa(len(value)) + ":" + value))
}

func writeTyped(digest hash.Hash, value any, depth int) error {
	if depth > 80 {
		return fmt.Errorf("structured result nesting exceeds budget")
	}
	switch v := value.(type) {
	case nil:
		_, _ = digest.Write([]byte("n;"))
	case bool:
		if v {
			_, _ = digest.Write([]byte("b1;"))
		} else {
			_, _ = digest.Write([]byte("b0;"))
		}
	case string:
		frame(digest, "s", v)
	case json.Number:
		number, err := decimalNumber(v.String())
		if err != nil {
			return err
		}
		frame(digest, "d", number)
	case []any:
		_, _ = digest.Write([]byte("a" + strconv.Itoa(len(v)) + ":"))
		for _, child := range v {
			if err := writeTyped(digest, child, depth+1); err != nil {
				return err
			}
		}
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		_, _ = digest.Write([]byte("o" + strconv.Itoa(len(v)) + ":"))
		for _, key := range keys {
			frame(digest, "s", key)
			if err := writeTyped(digest, v[key], depth+1); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unsupported structured result digest type")
	}
	return nil
}

func decimalNumber(text string) (string, error) {
	if len(text) > 10000 {
		return "", fmt.Errorf("structured result number exceeds budget")
	}
	negative := strings.HasPrefix(text, "-")
	if negative {
		text = text[1:]
	}
	parts := strings.FieldsFunc(text, func(r rune) bool { return r == 'e' || r == 'E' })
	if len(parts) < 1 || len(parts) > 2 {
		return "", fmt.Errorf("invalid structured result number")
	}
	exponent := 0
	if len(parts) == 2 {
		var err error
		exponent, err = strconv.Atoi(parts[1])
		if err != nil || exponent < -10000 || exponent > 10000 {
			return "", fmt.Errorf("structured result exponent exceeds budget")
		}
	}
	base := parts[0]
	dot := strings.IndexByte(base, '.')
	if dot < 0 {
		dot = len(base)
	}
	digits := strings.ReplaceAll(base, ".", "")
	for _, ch := range digits {
		if ch < '0' || ch > '9' {
			return "", fmt.Errorf("invalid structured result number")
		}
	}
	point := dot + exponent
	if point <= 0 {
		digits = "0." + strings.Repeat("0", -point) + digits
	} else if point >= len(digits) {
		digits += strings.Repeat("0", point-len(digits))
	} else {
		digits = digits[:point] + "." + digits[point:]
	}
	if strings.Contains(digits, ".") {
		digits = strings.TrimRight(strings.TrimRight(digits, "0"), ".")
	}
	digits = strings.TrimLeft(digits, "0")
	if strings.HasPrefix(digits, ".") {
		digits = "0" + digits
	}
	if digits == "" {
		return "0", nil
	}
	if negative {
		digits = "-" + digits
	}
	return digits, nil
}
