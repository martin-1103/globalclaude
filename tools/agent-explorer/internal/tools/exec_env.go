package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func runCommandWithEnv(ctx context.Context, timeoutSeconds int, dir string, env []string, name string, args ...string) (string, string, error) {
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), env...)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	out := strings.TrimSpace(stdout.String())
	errOut := strings.TrimSpace(stderr.String())
	if cctx.Err() == context.DeadlineExceeded {
		return out, errOut, fmt.Errorf("%s timed out", name)
	}
	return out, errOut, err
}
