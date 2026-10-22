package file

import "os"

// IsExist reports whether path can be stat'ed. A path that cannot be checked
// (for example under a regular file, or without permission) counts as
// missing, so callers go on to create it and report the real error.
func IsExist(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
