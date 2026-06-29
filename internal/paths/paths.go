package paths

import "path/filepath"

func DefaultConfigPath(projectRoot string) string {
	return filepath.Join(projectRoot, "config.json")
}

func DefaultLogDir(projectRoot string) string {
	return filepath.Join(projectRoot, "log")
}
