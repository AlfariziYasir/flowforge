package logger

import (
	"net/url"
	"strings"
)

// RedactURL parses a raw connection string/URL and redacts any userinfo passwords with "*****".
func RedactURL(rawURL string) string {
	if rawURL == "" {
		return ""
	}

	u, err := url.Parse(rawURL)
	if err != nil || u.User == nil {
		return rawURL
	}

	if _, hasPassword := u.User.Password(); hasPassword {
		u.User = url.UserPassword(u.User.Username(), "*****")
		return strings.Replace(u.String(), "%2A%2A%2A%2A%2A", "*****", 1)
	}

	return rawURL
}
