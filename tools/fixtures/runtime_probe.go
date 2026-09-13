// This test probe is mounted into the runtime container; it is never shipped.
package main

import (
	"crypto/x509"
	"fmt"
	"os"
	"path/filepath"
)

func require(ok bool, reason string) {
	if !ok {
		fmt.Fprintln(os.Stderr, reason)
		os.Exit(1)
	}
}
func main() {
	require(len(os.Args) == 2, "expected probe mode")
	switch os.Args[1] {
	case "permissions":
		require(os.Getuid() == 10001 && os.Getgid() == 10001, "unexpected runtime identity")
		file, err := os.CreateTemp("/usr/local", "runnerscout-permissions-")
		if err == nil {
			file.Close()
			os.Remove(file.Name())
		}
		require(err != nil, "runtime root is writable")
		file, err = os.CreateTemp("/tmp", "runnerscout-permissions-")
		require(err == nil, "scratch is not writable")
		name := file.Name()
		require(file.Close() == nil, "scratch close failed")
		require(os.Remove(name) == nil, "scratch cleanup failed")
	case "dependencies":
		for _, pattern := range []string{"/usr/local/aws-cli", "/opt/google-cloud-sdk", "/usr/bin/python*", "/usr/local/bin/python*", "/usr/local/lib/python*", "/usr/lib/python*", "/bin/sh", "/bin/bash", "/usr/bin/aws", "/usr/local/bin/aws", "/usr/bin/az", "/usr/local/bin/az", "/usr/bin/gcloud"} {
			matches, err := filepath.Glob(pattern)
			require(err == nil && len(matches) == 0, "unexpected CLI/interpreter dependency: "+pattern)
		}
	case "trust":
		pool, err := x509.SystemCertPool()
		require(err == nil && pool != nil && len(pool.Subjects()) > 0, "system TLS trust store missing")
	default:
		require(false, "unknown probe mode")
	}
	fmt.Println(os.Args[1] + ": pass")
}
