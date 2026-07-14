// The serve subcommand: run the verification sidecar.
package cli

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/JaydenCJ/keyturn/internal/server"
	"github.com/JaydenCJ/keyturn/internal/version"
)

// DefaultAddr is the loopback address the sidecar binds by default.
const DefaultAddr = "127.0.0.1:7710"

func (a *App) cmdServe(args []string) int {
	fs, storePath := a.newFlagSet("serve")
	addr := fs.String("addr", DefaultAddr, "listen address")
	adminToken := fs.String("admin-token", "",
		"bearer token for the /v1/keys admin API (default $KEYTURN_ADMIN_TOKEN; empty disables it)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() != 0 {
		return a.usageErr(fmt.Errorf("serve takes no positional arguments"))
	}
	token := *adminToken
	if token == "" {
		token = a.getenv("KEYTURN_ADMIN_TOKEN")
	}
	st, ok := a.openStore(a.storePath(*storePath))
	if !ok {
		return ExitRuntime
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return a.runtimeErr(err)
	}
	defer ln.Close()

	srv := &server.Server{
		Store:      st,
		AdminToken: token,
		Rand:       a.Rand,
		Now:        a.Now,
		Logger:     log.New(a.Stderr, "keyturn: ", log.LstdFlags),
	}

	noun := "keys"
	if st.Len() == 1 {
		noun = "key"
	}
	fmt.Fprintf(a.Stdout, "keyturn %s listening on http://%s (store: %s, %d %s)\n",
		version.Version, ln.Addr(), st.Path(), st.Len(), noun)
	if token == "" {
		fmt.Fprintln(a.Stdout, "admin API: disabled (set --admin-token or KEYTURN_ADMIN_TOKEN to enable)")
	} else {
		fmt.Fprintln(a.Stdout, "admin API: enabled at /v1/keys")
	}
	if !isLoopback(ln.Addr()) {
		fmt.Fprintln(a.Stderr,
			"keyturn: warning: binding a non-loopback address — the verify endpoint is unauthenticated by design; keep it behind your proxy")
	}

	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return a.runtimeErr(err)
	}
	return ExitOK
}

func isLoopback(addr net.Addr) bool {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return false
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}
