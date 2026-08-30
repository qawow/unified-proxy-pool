//go:build !unix

package update

import "fmt"

func execSelf(argv0 string, argv []string, envv []string) error {
	return fmt.Errorf("hot update exec is not supported on this OS")
}
