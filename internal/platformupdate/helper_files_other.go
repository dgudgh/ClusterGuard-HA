//go:build !linux && !darwin

package platformupdate

import (
	"errors"
	"os"
)

var errHelperFilesystemUnsupported = errors.New("secure update helper filesystem operations require Linux or Darwin")

func openHelperDirectory(string) (*os.File, error) {
	return nil, errHelperFilesystemUnsupported
}

func openHelperFile(*os.File, string, int, os.FileMode) (*os.File, error) {
	return nil, errHelperFilesystemUnsupported
}

func writeHelperJob(*os.File, Job) error {
	return errHelperFilesystemUnsupported
}
