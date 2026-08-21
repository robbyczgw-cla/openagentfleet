// Package testexe writes helper programs that Go's exec.LookPath can launch
// on Unix and Windows. Windows rejects extensionless POSIX scripts, so helpers
// are emitted as .cmd files there.
package testexe

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func Path(dir, name string) string {
	if runtime.GOOS == "windows" && filepath.Ext(name) == "" {
		return filepath.Join(dir, name+".cmd")
	}
	return filepath.Join(dir, name)
}

func Write(t testing.TB, path, posixScript, cmdScript string) {
	t.Helper()
	body := posixScript
	if runtime.GOOS == "windows" {
		if strings.TrimSpace(cmdScript) == "" {
			t.Skip("POSIX process helper is not available on Windows")
		}
		body = cmdScript
	}
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
}

func WriteEcho(t testing.TB, dir, name, line string) string {
	t.Helper()
	path := Path(dir, name)
	if runtime.GOOS == "windows" {
		Write(t, path, "", "@echo off\r\necho "+line+"\r\n")
	} else {
		Write(t, path, "#!/bin/sh\nprintf '%s\\n' "+shQuote(line)+"\n", "")
	}
	return path
}

func WritePowerShell(t testing.TB, path, posixScript, psScript string) {
	t.Helper()
	if runtime.GOOS != "windows" {
		Write(t, path, posixScript, "")
		return
	}
	if strings.TrimSpace(psScript) == "" {
		t.Skip("POSIX process helper is not available on Windows")
	}
	psPath := strings.TrimSuffix(path, ".cmd") + ".ps1"
	if err := os.WriteFile(psPath, []byte(psScript+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	launcher := "@echo off\r\npowershell.exe -NoProfile -ExecutionPolicy Bypass -File \"%~dp0" + filepath.Base(psPath) + "\" %*\r\n"
	if err := os.WriteFile(path, []byte(launcher), 0o700); err != nil {
		t.Fatal(err)
	}
}

func WriteReexec(t testing.TB, dir, name, testRun, argsPath string, env map[string]string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := Path(dir, name)
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if runtime.GOOS == "windows" {
		var b strings.Builder
		b.WriteString("@echo off\r\nsetlocal\r\n")
		if argsPath != "" {
			fmt.Fprintf(&b, "> \"%s\" (\r\n  for %%%%A in (%%*) do echo(%%%%A\r\n)\r\n", argsPath)
		}
		for _, key := range keys {
			fmt.Fprintf(&b, "set %s=%s\r\n", key, env[key])
		}
		fmt.Fprintf(&b, "\"%s\" -test.run \"%s\"\r\nexit /b %%ERRORLEVEL%%\r\n", self, testRun)
		Write(t, path, "", b.String())
		return path
	}
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	if argsPath != "" {
		fmt.Fprintf(&b, "printf '%%s\\n' \"$@\" > %s\n", shQuote(argsPath))
	}
	for _, key := range keys {
		fmt.Fprintf(&b, "export %s=%s\n", key, shQuote(env[key]))
	}
	fmt.Fprintf(&b, "exec %s -test.run %s\n", shQuote(self), shQuote(testRun))
	Write(t, path, b.String(), "")
	return path
}

func shQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}
