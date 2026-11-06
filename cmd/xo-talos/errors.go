package main

import (
	"regexp"
	"strings"
)

var (
	// The SDK formats some errors with a surplus argument, which fmt renders
	// as %!(EXTRA string=<url>); the URL is already in the message.
	fmtExtra = regexp.MustCompile(`%!\(EXTRA [^)]*\)`)
	// XO API errors embed the indented JSON body of the response.
	spaces = regexp.MustCompile(`\s*\n\s*`)
)

// cleanError renders err on one line, without the SDK's formatting debris.
func cleanError(err error) string {
	return cleanLine(err.Error())
}

func cleanLine(s string) string {
	s = fmtExtra.ReplaceAllString(s, "")
	return strings.TrimSpace(spaces.ReplaceAllString(s, " "))
}
