package pipe

import (
	"encoding/base64"
	"strings"
)

// Protocol prefixes
const (
	PrefixERR  = "ERR|"
	PrefixTAIL = "TAIL|"
	PrefixPONG = "PONG|"
)

// Base64Encode encodes text to base64.
func Base64Encode(text string) string {
	return base64.StdEncoding.EncodeToString([]byte(text))
}

// Base64Decode decodes a base64 string.
func Base64Decode(encoded string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// IsError checks if the response has an ERR| prefix.
// Returns the error message and true if it is an error response.
func IsError(resp string) (string, bool) {
	resp = strings.TrimSpace(resp)
	if strings.HasPrefix(resp, PrefixERR) {
		return strings.TrimPrefix(resp, PrefixERR), true
	}
	return "", false
}

// StripTailHeader strips the first line (TAIL|<session>|<linecount>)
// from a multi-line response and returns the remaining content.
func StripTailHeader(resp string) string {
	idx := strings.Index(resp, "\n")
	if idx < 0 {
		return ""
	}
	return resp[idx+1:]
}
