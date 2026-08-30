//go:build unix

package update

import "syscall"

func execSelf(argv0 string, argv []string, envv []string) error {
	return syscall.Exec(argv0, argv, envv)
}
