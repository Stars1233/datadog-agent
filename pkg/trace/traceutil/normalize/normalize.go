// Unless explicitly stated otherwise all files in this repository are licensed
// under the Apache License Version 2.0.
// This product includes software developed at Datadog (https://www.datadoghq.com/).
// Copyright 2016-present Datadog, Inc.

// Package normalize contains functions for normalizing trace data.
package normalize

import (
	"errors"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

const (
	// DefaultSpanName is the default name we assign a span if it's missing and we have no reasonable fallback
	DefaultSpanName = "unnamed_operation"
	// DefaultServiceName is the default name we assign a service if it's missing and we have no reasonable fallback
	DefaultServiceName = "unnamed-service"
)

const (
	// MaxNameLen the maximum length a name can have
	MaxNameLen = 100
	// MaxServiceLen the maximum length a service can have
	MaxServiceLen = 100
	// MaxResourceLen the maximum length a resource can have
	MaxResourceLen = 5000
)

var (
	// ErrEmpty specifies that the passed input was empty.
	ErrEmpty = errors.New("empty")
	// ErrTooLong signifies that the input was too long.
	ErrTooLong = errors.New("too long")
	// ErrInvalid signifies that the input was invalid.
	ErrInvalid = errors.New("invalid")
)

var isAlphaLookup = [256]bool{}
var isAlphaNumLookup = [256]bool{}
var isValidASCIIStartCharLookup = [256]bool{}
var isValidASCIITagCharLookup = [256]bool{}

func init() {
	for i := 0; i < 256; i++ {
		isAlphaLookup[i] = isAlpha(byte(i))
		isAlphaNumLookup[i] = isAlphaNum(byte(i))
		isValidASCIIStartCharLookup[i] = isValidASCIIStartChar(byte(i))
		isValidASCIITagCharLookup[i] = isValidASCIITagChar(byte(i))
	}
}

// NormalizeName normalizes a span name and returns an error describing the reason
// (if any) why the name was modified.
//
//nolint:revive
func NormalizeName(name string) (string, error) {
	if name == "" {
		return DefaultSpanName, ErrEmpty
	}
	var err error
	if len(name) > MaxNameLen {
		name = TruncateUTF8(name, MaxNameLen)
		err = ErrTooLong
	}
	name, ok := normMetricNameParse(name)
	if !ok {
		return DefaultSpanName, ErrInvalid
	}
	return name, err
}

// NormalizeService normalizes a span service and returns an error describing the reason
// (if any) why the name was modified.
//
//nolint:revive
func NormalizeService(svc string, lang string) (string, error) {
	if svc == "" {
		return fallbackService(lang), ErrEmpty
	}
	var err error
	if len(svc) > MaxServiceLen {
		svc = TruncateUTF8(svc, MaxServiceLen)
		err = ErrTooLong
	}
	// We are normalizing just the tag value.
	s := NormalizeTagValue(svc)
	if s == "" {
		return fallbackService(lang), ErrInvalid
	}
	return s, err
}

// NormalizePeerService normalizes a span's peer.service and returns an error describing the reason
// (if any) why the name was modified.
//
//nolint:revive
func NormalizePeerService(svc string) (string, error) {
	if svc == "" {
		return "", nil
	}
	var err error
	if len(svc) > MaxServiceLen {
		svc = TruncateUTF8(svc, MaxServiceLen)
		err = ErrTooLong
	}
	// We are normalizing just the tag value.
	s := NormalizeTagValue(svc)
	if s == "" {
		return "", ErrInvalid
	}
	return s, err
}

// fallbackServiceNames is a cache of default service names to use
// when the span's service is unset or invalid.
var fallbackServiceNames sync.Map

// fallbackService returns the fallback service name for a service
// belonging to language lang.
func fallbackService(lang string) string {
	if lang == "" {
		return DefaultServiceName
	}
	if v, ok := fallbackServiceNames.Load(lang); ok {
		return v.(string)
	}
	var str strings.Builder
	str.WriteString("unnamed-")
	str.WriteString(lang)
	str.WriteString("-service")
	fallbackServiceNames.Store(lang, str.String())
	return str.String()
}

const maxTagLength = 200

// NormalizeTag applies some normalization to ensure the full tag_key:tag_value string matches the backend requirements.
//
//nolint:revive
func NormalizeTag(v string) string {
	return normalize(v, true)
}

// NormalizeTagValue applies some normalization to ensure the tag value matches the backend requirements.
// It should be used for cases where we have just the tag_value as the input (instead of tag_key:tag_value).
//
//nolint:revive
func NormalizeTagValue(v string) string {
	return normalize(v, false)
}

func normalize(v string, removeDigitStartChar bool) string {
	// Fast path: Check if the tag is valid and only contains ASCII characters,
	// if yes return it as-is right away. For most use-cases this reduces CPU usage.
	if isNormalizedASCIITag(v, removeDigitStartChar) {
		return v
	}
	// the algorithm works by creating a set of cuts marking start and end offsets in v
	// that have to be replaced with underscore (_)
	if len(v) == 0 {
		return ""
	}
	var (
		trim  int      // start character (if trimming)
		cuts  [][2]int // sections to discard: (start, end) pairs
		chars int      // number of characters processed
	)
	var (
		i    int  // current byte
		r    rune // current rune
		jump int  // tracks how many bytes the for range advances on its next iteration
	)
	tag := []byte(v)
	for i, r = range v {
		jump = utf8.RuneLen(r) // next i will be i+jump
		if r == utf8.RuneError {
			// On invalid UTF-8, the for range advances only 1 byte (see: https://golang.org/ref/spec#For_range (point 2)).
			// However, utf8.RuneError is equivalent to unicode.ReplacementChar so we should rely on utf8.DecodeRune to tell
			// us whether this is an actual error or just a unicode.ReplacementChar that was present in the string.
			_, width := utf8.DecodeRune(tag[i:])
			jump = width
		}
		// fast path; all letters (and colons) are ok
		switch {
		case r >= 'a' && r <= 'z' || r == ':':
			chars++
			goto end
		case r >= 'A' && r <= 'Z':
			// lower-case
			tag[i] += 'a' - 'A'
			chars++
			goto end
		}
		if unicode.IsUpper(r) {
			// lowercase this character
			if low := unicode.ToLower(r); utf8.RuneLen(r) == utf8.RuneLen(low) {
				// but only if the width of the lowercased character is the same;
				// there are some rare edge-cases where this is not the case, such
				// as \u017F (ſ)
				utf8.EncodeRune(tag[i:], low)
				r = low
			}
		}
		switch {
		case unicode.IsLetter(r):
			chars++
		// If it's not a unicode letter, and it's the first char, and digits are allowed for the start char,
		// we should goto end because the remaining cases are not valid for a start char.
		case removeDigitStartChar && chars == 0:
			trim = i + jump
			goto end
		case unicode.IsDigit(r) || r == '.' || r == '/' || r == '-':
			chars++
		default:
			// illegal character
			chars++
			if n := len(cuts); n > 0 && cuts[n-1][1] >= i {
				// merge intersecting cuts
				cuts[n-1][1] += jump
			} else {
				// start a new cut
				cuts = append(cuts, [2]int{i, i + jump})
			}
		}
	end:
		if i+jump >= 2*maxTagLength {
			// bail early if the tag contains a lot of non-letter/digit characters.
			// If a tag is test🍣🍣[...]🍣, then it's unlikely to be a properly formatted tag
			break
		}
		if chars >= maxTagLength {
			// we've reached the maximum
			break
		}
	}

	tag = tag[trim : i+jump] // trim start and end
	if len(cuts) == 0 {
		// tag was ok, return it as it is
		return string(tag)
	}
	delta := trim // cut offsets delta
	for _, cut := range cuts {
		// start and end of cut, including delta from previous cuts:
		start, end := cut[0]-delta, cut[1]-delta

		if end >= len(tag) {
			// this cut includes the end of the string; discard it
			// completely and finish the loop.
			tag = tag[:start]
			break
		}
		// replace the beginning of the cut with '_'
		tag[start] = '_'
		if end-start == 1 {
			// nothing to discard
			continue
		}
		// discard remaining characters in the cut
		copy(tag[start+1:], tag[end:])

		// shorten the slice
		tag = tag[:len(tag)-(end-start)+1]

		// count the new delta for future cuts
		delta += cut[1] - cut[0] - 1
	}
	return string(tag)
}

// This code is borrowed from dd-go metric normalization

// fast isAlpha for ascii
func isAlpha(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// fast isAlphaNumeric for ascii
func isAlphaNum(b byte) bool {
	return isAlpha(b) || (b >= '0' && b <= '9')
}

func isValidNormalizedMetricName(name string) bool {
	if name == "" {
		return false
	}
	if !isAlphaLookup[name[0]] {
		return false
	}
	for j := 1; j < len(name); j++ {
		b := name[j]
		if !(isAlphaNumLookup[b] || (b == '.' && !(name[j-1] == '_')) || (b == '_' && !(name[j-1] == '_'))) {
			return false
		}
	}
	return true
}

// normMetricNameParse normalizes metric names with a parser instead of using
// garbage-creating string replacement routines.
func normMetricNameParse(name string) (string, bool) {
	if name == "" || len(name) > MaxNameLen {
		return name, false
	}

	var i, ptr int
	var resa [MaxNameLen]byte
	res := resa[:0]

	// skip non-alphabetic characters
	for ; i < len(name) && !isAlphaLookup[name[i]]; i++ {
		continue
	}

	// if there were no alphabetic characters it wasn't valid
	if i == len(name) {
		return "", false
	}

	if isValidNormalizedMetricName(name[i:]) {
		normalized := name[i:]
		if normalized[len(normalized)-1] == '_' {
			normalized = normalized[:len(normalized)-1]
		}
		return normalized, true
	}

	for ; i < len(name); i++ {
		switch {
		case isAlphaNumLookup[name[i]]:
			res = append(res, name[i])
			ptr++
		case name[i] == '.':
			// we skipped all non-alpha chars up front so we have seen at least one
			switch res[ptr-1] {
			// overwrite underscores that happen before periods
			case '_':
				res[ptr-1] = '.'
			default:
				res = append(res, '.')
				ptr++
			}
		default:
			// we skipped all non-alpha chars up front so we have seen at least one
			switch res[ptr-1] {
			// no double underscores, no underscores after periods
			case '.', '_':
			default:
				res = append(res, '_')
				ptr++
			}
		}
	}

	if res[ptr-1] == '_' {
		res = res[:ptr-1]
	}

	return string(res), true
}

func isNormalizedASCIITag(tag string, checkValidStartChar bool) bool {
	if len(tag) == 0 {
		return true
	}
	if len(tag) > maxTagLength {
		return false
	}
	i := 0
	if checkValidStartChar {
		if !isValidASCIIStartCharLookup[tag[0]] {
			return false
		}
		i++
	}
	for ; i < len(tag); i++ {
		b := tag[i]
		// TODO: Attempt to optimize this check using SIMD/vectorization.
		if isValidASCIITagCharLookup[b] {
			continue
		}
		if b == '_' {
			// an underscore is only okay if followed by a valid non-underscore character
			i++
			if i == len(tag) || !isValidASCIITagCharLookup[tag[i]] {
				return false
			}
		} else {
			return false
		}
	}
	return true
}

func isValidASCIIStartChar(c byte) bool {
	return ('a' <= c && c <= 'z') || c == ':'
}

func isValidASCIITagChar(c byte) bool {
	return isValidASCIIStartChar(c) || ('0' <= c && c <= '9') || c == '.' || c == '/' || c == '-'
}
