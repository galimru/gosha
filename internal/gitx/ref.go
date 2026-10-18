package gitx

import (
	"errors"
	"fmt"
	"strings"
)

// CheckBranchName applies the rules of `git check-ref-format --branch`
// so that config validation doesn't need a git process.
func CheckBranchName(name string) error {
	bad := func(why string) error { return fmt.Errorf("invalid branch name %q: %s", name, why) }
	switch {
	case name == "":
		return errors.New("invalid branch name: empty")
	case name == "@":
		return bad(`cannot be "@"`)
	case strings.HasPrefix(name, "-"):
		return bad(`cannot start with "-"`)
	case strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/"):
		return bad(`cannot start or end with "/"`)
	case strings.HasSuffix(name, "."):
		return bad(`cannot end with "."`)
	case strings.Contains(name, ".."):
		return bad(`cannot contain ".."`)
	case strings.Contains(name, "//"):
		return bad(`cannot contain "//"`)
	case strings.Contains(name, "@{"):
		return bad(`cannot contain "@{"`)
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r) {
			return bad(fmt.Sprintf("cannot contain %q", r))
		}
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") {
			return bad(`a path component cannot start with "."`)
		}
		if strings.HasSuffix(part, ".lock") {
			return bad(`a path component cannot end with ".lock"`)
		}
	}
	return nil
}
