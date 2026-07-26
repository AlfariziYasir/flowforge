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

	if pass, hasPassword := u.User.Password(); hasPassword && pass != "" {
		return strings.Replace(rawURL, ":"+pass+"@", ":*****@", 1)
	}

	return rawURL
}
