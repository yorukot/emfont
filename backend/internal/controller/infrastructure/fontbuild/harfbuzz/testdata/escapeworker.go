package main

import (
	"io"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "descendant" {
		if _, err := syscall.Setsid(); err != nil {
			os.Exit(2)
		}
		_ = os.WriteFile(os.Args[0]+".escaped", []byte(strconv.Itoa(os.Getpid())), 0o600)
		for {
			time.Sleep(time.Hour)
		}
	}

	_, _ = io.Copy(io.Discard, os.Stdin)
	descendant := exec.Command(os.Args[0], "descendant")
	descendant.Stdout = os.Stdout
	descendant.Stderr = os.Stderr
	if err := descendant.Start(); err != nil {
		os.Exit(3)
	}
	for {
		if _, err := os.Stat(os.Args[0] + ".escaped"); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	for {
		time.Sleep(time.Hour)
	}
}
