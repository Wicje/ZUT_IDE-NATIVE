//go:build !darwin

package main

// keepSystemCrashes keeps the system's reports of Zut's crashes, which
// only macOS writes.
func keepSystemCrashes(dir string) {}
