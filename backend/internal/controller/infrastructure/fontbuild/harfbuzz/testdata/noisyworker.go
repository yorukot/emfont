package main

import (
	"bytes"
	"os"
	"time"
)

func main() {
	_, _ = os.Stderr.Write(bytes.Repeat([]byte{'x'}, 1<<20))
	for {
		time.Sleep(time.Hour)
	}
}
