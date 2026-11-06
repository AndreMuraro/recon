package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		args       []string
		code       int
		stdout     string
		stderrPart string
	}{
		{name: "no command", args: nil, code: exitError, stderrPart: "Usage:"},
		{name: "help", args: []string{"help"}, code: exitOK, stdout: usage},
		{name: "help flag", args: []string{"--help"}, code: exitOK, stdout: usage},
		{name: "version", args: []string{"version"}, code: exitOK, stdout: "recon dev\n"},
		{name: "version help", args: []string{"version", "-h"}, code: exitOK, stderrPart: "Usage of version"},
		{name: "version extra argument", args: []string{"version", "x"}, code: exitError, stderrPart: `unexpected argument "x"`},
		{name: "unknown flag", args: []string{"version", "--nope"}, code: exitError, stderrPart: "flag provided but not defined"},
		{name: "unknown command", args: []string{"diff"}, code: exitError, stderrPart: `unknown command "diff"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			if code := run(tt.args, &stdout, &stderr); code != tt.code {
				t.Errorf("exit code = %d, want %d (stderr: %s)", code, tt.code, stderr.String())
			}
			if tt.stdout != "" && stdout.String() != tt.stdout {
				t.Errorf("stdout = %q, want %q", stdout.String(), tt.stdout)
			}
			if !strings.Contains(stderr.String(), tt.stderrPart) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.stderrPart)
			}
		})
	}
}
