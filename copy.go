package nb6

import (
	"io"
	"io/fs"
)

// copyFile copies src to dst like the cp command.
func copyFile(fsys FS, srcName, destName string) error {
	if destName == srcName {
		return fs.ErrInvalid
	}
	srcFile, err := fsys.Open(srcName)
	if err != nil {
		return err
	}
	defer srcFile.Close()
	srcFileInfo, err := srcFile.Stat()
	if err != nil {
		return err
	}
	destFile, err := fsys.OpenWriter(destName, srcFileInfo.Mode())
	if err != nil {
		return err
	}
	defer destFile.Close()
	_, err = io.Copy(destFile, srcFile)
	if err != nil {
		return err
	}
	return destFile.Close()
}
