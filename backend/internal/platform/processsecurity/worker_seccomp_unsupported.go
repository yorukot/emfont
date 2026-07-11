//go:build !linux || (!amd64 && !arm64)

package processsecurity

import "errors"

func ApplyFontWorkerSandbox() error {
	return errors.New("font worker process sandbox requires Linux on amd64 or arm64")
}
