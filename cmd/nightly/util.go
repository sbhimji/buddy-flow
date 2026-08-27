package main

import (
	"bufio"
	"encoding/json"
	"os"
	"regexp"
	"strings"
)

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

var datePrefix = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2})`)

// dateOf extracts the leading YYYY-MM-DD of a file or directory name.
func dateOf(name string) (string, bool) {
	m := datePrefix.FindStringSubmatch(name)
	if m == nil {
		return "", false
	}
	return m[1], true
}

// envValue reads KEY from the environment, then from the KEY=VALUE env file
// (the cmd/live contract). Never logged.
func envValue(envFile, key string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	f, err := os.Open(envFile)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if v, ok := strings.CutPrefix(line, key+"="); ok {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

func fileSize(path string) (int64, bool) {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return 0, false
	}
	return fi.Size(), true
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
