//go:build !unix

package conf

import "os"

func keepOwner(*os.File, os.FileInfo) {}
