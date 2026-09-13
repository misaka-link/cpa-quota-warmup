package main

import "path/filepath"

// globMatch reports whether name matches the shell-style glob pattern
// supported by path/filepath.Match (*, ?, and [] character classes). Auth
// file names never contain a path separator, so filepath.Match's separator
// handling never comes into play here. A malformed pattern never matches
// rather than panicking or erroring the whole config out.
func globMatch(pattern, name string) bool {
	if pattern == "" {
		return false
	}
	ok, err := filepath.Match(pattern, name)
	if err != nil {
		return false
	}
	return ok
}
