// Package cli implements the keyturn command line.
//
// The App struct carries every ambient dependency — writers, clock,
// randomness, environment — so tests run the real CLI in-process with
// pinned time and deterministic keys, and main.go stays four lines.
package cli

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/JaydenCJ/keyturn/internal/store"
	"github.com/JaydenCJ/keyturn/internal/version"
)

// Exit codes, kept scriptable and stable.
const (
	ExitOK      = 0 // success / key is valid
	ExitDenied  = 1 // verify: key is definitively not valid
	ExitUsage   = 2 // bad flags or arguments
	ExitRuntime = 3 // I/O, store, or server failure
)

// DefaultStore is the store path when neither --store nor
// KEYTURN_STORE is set.
const DefaultStore = "keyturn.json"

// App is the CLI with all its dependencies injected.
type App struct {
	Stdout io.Writer
	Stderr io.Writer
	// Now is the clock used for issuing, expiry, and rate limiting.
	Now func() time.Time
	// Rand feeds key generation; nil means crypto/rand.
	Rand io.Reader
	// Getenv resolves environment variables; nil means os.Getenv.
	Getenv func(string) string
}

func (a *App) getenv(name string) string {
	if a.Getenv != nil {
		return a.Getenv(name)
	}
	return os.Getenv(name)
}

func (a *App) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// Run dispatches os.Args[1:]-style arguments and returns an exit code.
func (a *App) Run(args []string) int {
	if len(args) == 0 {
		a.usage(a.Stderr)
		return ExitUsage
	}
	switch args[0] {
	case "create":
		return a.cmdCreate(args[1:])
	case "list":
		return a.cmdList(args[1:])
	case "show":
		return a.cmdShow(args[1:])
	case "revoke":
		return a.cmdSetDisabled(args[1:], true)
	case "enable":
		return a.cmdSetDisabled(args[1:], false)
	case "delete":
		return a.cmdDelete(args[1:])
	case "verify":
		return a.cmdVerify(args[1:])
	case "serve":
		return a.cmdServe(args[1:])
	case "version", "--version", "-v":
		fmt.Fprintf(a.Stdout, "keyturn %s\n", version.Version)
		return ExitOK
	case "help", "--help", "-h":
		a.usage(a.Stdout)
		return ExitOK
	default:
		fmt.Fprintf(a.Stderr, "keyturn: unknown command %q\n\n", args[0])
		a.usage(a.Stderr)
		return ExitUsage
	}
}

func (a *App) usage(w io.Writer) {
	fmt.Fprintf(w, `keyturn %s — API key sidecar: issue, hash, scope and rate-limit keys.

Usage:
  keyturn create --name NAME [--scopes s1,s2] [--rate N/win] [--burst N]
                 [--label L] [--expires WHEN] [--meta k=v]... [--quiet]
  keyturn list   [--format text|json]
  keyturn show   ID
  keyturn revoke ID
  keyturn enable ID
  keyturn delete ID
  keyturn verify KEY [--scopes s1,s2] [--cost N] [--format text|json]
  keyturn serve  [--addr 127.0.0.1:7710] [--admin-token TOKEN]
  keyturn version

Every command reads --store PATH (default $KEYTURN_STORE or %s).
create, list, show and verify accept --format text|json.
Exit codes: 0 ok/valid, 1 key denied, 2 usage, 3 runtime error.
`, version.Version, DefaultStore)
}

// storePath resolves the store path from a flag value or environment.
func (a *App) storePath(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := a.getenv("KEYTURN_STORE"); env != "" {
		return env
	}
	return DefaultStore
}

// openStore loads the store, mapping failure to a runtime-error print.
func (a *App) openStore(path string) (*store.Store, bool) {
	st, err := store.Open(path)
	if err != nil {
		fmt.Fprintf(a.Stderr, "keyturn: %v\n", err)
		return nil, false
	}
	return st, true
}

func (a *App) usageErr(err error) int {
	fmt.Fprintf(a.Stderr, "keyturn: %v\n", err)
	return ExitUsage
}

func (a *App) runtimeErr(err error) int {
	fmt.Fprintf(a.Stderr, "keyturn: %v\n", err)
	return ExitRuntime
}
