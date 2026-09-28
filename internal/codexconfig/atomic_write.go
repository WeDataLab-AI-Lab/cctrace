package codexconfig

import (
	"os"

	"cctrace/internal/atomicfile"
)

func writeConfigFileAtomic(path string, data []byte, perm os.FileMode) (err error) {
	return atomicfile.Write(path, data, perm)
}
