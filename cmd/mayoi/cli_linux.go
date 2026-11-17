package main

import "os"

// commandLine reports whether Zut was started as the mayoi command,
// which on Linux is Zut itself: with arguments, unless detach or a
// snapshot started it.
func commandLine() bool {
	return os.Getenv(cliEnv) != "" || len(os.Args) > 1 && os.Getenv(detachedEnv) == "" && os.Getenv("MAYOI_SNAPSHOT") == ""
}

// attachConsole does nothing: Linux starts with the command's handles.
func attachConsole() {}

// startApp starts Zut apart from the command, opening paths.
func startApp(paths []string) error { return startDetached(paths, detachedEnv+"=1") }

// refreshCLI does nothing: the command is Zut itself.
func refreshCLI() {}
