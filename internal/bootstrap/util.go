package bootstrap

import (
	"os"
	"strings"

	"github.com/shadowsafin/corerouter/internal/routing"
)

// osGetenv reads an environment variable.
//
// It is a named wrapper rather than a direct call so the dependency is visible in
// one place, which matters because reading the environment is exactly the kind of
// hidden coupling that makes a package hard to test.
func osGetenv(name string) string { return os.Getenv(name) }

// trimSpace trims surrounding whitespace.
func trimSpace(s string) string { return strings.TrimSpace(s) }

// Compile-time assertions that the wiring satisfies the interfaces declared by the
// consuming packages. These are placed here rather than in the packages that
// define the interfaces so the dependency points inward: bootstrap knows about
// routing, and routing knows nothing about bootstrap.
var (
	_ routing.Availability = (*RegistryAvailability)(nil)
)
