package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "descendant" {
		for {
			time.Sleep(time.Hour)
		}
	}

	request, _ := io.ReadAll(os.Stdin)
	executable, err := os.Executable()
	if err != nil {
		os.Exit(2)
	}
	descendant := exec.Command(executable, "descendant")
	descendant.Stdout = os.Stdout
	descendant.Stderr = os.Stderr
	if err := descendant.Start(); err != nil {
		os.Exit(3)
	}
	parentGroup, _ := syscall.Getpgid(os.Getpid())
	descendantGroup, _ := syscall.Getpgid(descendant.Process.Pid)
	state := []string{
		strconv.Itoa(os.Getpid()),
		strconv.Itoa(descendant.Process.Pid),
		strconv.Itoa(parentGroup),
		strconv.Itoa(descendantGroup),
	}
	if err := os.WriteFile(executable+".pids", []byte(strings.Join(state, " ")), 0o600); err != nil {
		os.Exit(4)
	}

	if bytes.HasSuffix(request, []byte("malformed")) {
		_, _ = os.Stdout.Write(make([]byte, 36))
	}
	for {
		time.Sleep(time.Hour)
	}
}
