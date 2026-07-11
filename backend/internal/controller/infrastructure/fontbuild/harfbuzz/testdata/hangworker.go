package main

import (
	"os"
	"strconv"
	"time"
)

func main() {
	executable, err := os.Executable()
	if err == nil {
		_ = os.WriteFile(executable+".pid", []byte(strconv.Itoa(os.Getpid())), 0o600)
		if os.Getenv("EMFONT_TEST_WORKER_SECRET") != "" {
			_ = os.WriteFile(executable+".leaked", []byte("inherited"), 0o600)
		}
	}
	for {
		time.Sleep(time.Hour)
	}
}
