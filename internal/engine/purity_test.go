package engine_test

import (
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// forbiddenImports are packages that must never reach internal/engine. The engine
// is pure domain logic: it is handed values and returns values. Anything here would
// make it untestable without I/O and would invert the dependency direction that
// Phase 4's executor relies on.
var forbiddenImports = []string{
	"net/http",
	"database/sql",
	"github.com/jackc/pgx",
	"github.com/redis/go-redis",
	"github.com/Masterminds/squirrel",
	"flowforge/internal/workflow",
	"flowforge/internal/auth",
	"flowforge/internal/platform",
}

// allowedPrefixes are the only non-stdlib imports the engine may use.
var allowedPrefixes = []string{
	"flowforge/internal/domain",
}

// T-26: the engine's purity contract, enforced mechanically rather than by review.
func TestEngine_ImportPurity(t *testing.T) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", nil, parser.ImportsOnly)
	require.NoError(t, err)

	for pkgName, pkg := range pkgs {
		// Test files may import testify and friends; the contract binds the
		// production package only.
		if strings.HasSuffix(pkgName, "_test") {
			continue
		}

		for fileName, file := range pkg.Files {
			if strings.HasSuffix(fileName, "_test.go") {
				continue
			}

			for _, spec := range file.Imports {
				path, err := strconv.Unquote(spec.Path.Value)
				require.NoError(t, err)

				for _, forbidden := range forbiddenImports {
					require.False(t, strings.HasPrefix(path, forbidden),
						"%s imports %q, which breaks the engine purity contract", fileName, path)
				}

				if !strings.Contains(path, ".") {
					continue // stdlib
				}

				var allowed bool
				for _, prefix := range allowedPrefixes {
					if strings.HasPrefix(path, prefix) {
						allowed = true
						break
					}
				}
				require.True(t, allowed,
					"%s imports %q; internal/engine may import only the standard library and %v",
					fileName, path, allowedPrefixes)
			}
		}
	}
}
