package awsini

import "os"

func writeFileImpl(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}

func chmod(path string, mode os.FileMode) error {
	return os.Chmod(path, mode)
}
