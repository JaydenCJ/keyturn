// Key management and verification subcommands.
package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/JaydenCJ/keyturn/internal/issue"
	"github.com/JaydenCJ/keyturn/internal/server"
	"github.com/JaydenCJ/keyturn/internal/store"
	"github.com/JaydenCJ/keyturn/internal/verify"
)

// metaFlag collects repeatable --meta k=v pairs.
type metaFlag map[string]string

func (m metaFlag) String() string { return fmt.Sprintf("%v", map[string]string(m)) }

func (m metaFlag) Set(v string) error {
	eq := strings.IndexByte(v, '=')
	if eq <= 0 {
		return fmt.Errorf("want k=v, got %q", v)
	}
	m[v[:eq]] = v[eq+1:]
	return nil
}

// splitScopes turns "a,b , c" into ["a","b","c"], dropping empties.
func splitScopes(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// parseMixed parses flags that may appear before or after positional
// arguments (stdlib flag stops at the first positional; users expect
// `keyturn verify KEY --scopes x` to work). It re-parses the tail after
// each positional and returns the positionals in order.
func parseMixed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

func (a *App) newFlagSet(name string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.Stderr)
	storePath := fs.String("store", "", "store file (default $KEYTURN_STORE or "+DefaultStore+")")
	return fs, storePath
}

func (a *App) cmdCreate(args []string) int {
	fs, storePath := a.newFlagSet("create")
	name := fs.String("name", "", "human-readable key name (required)")
	label := fs.String("label", "", "label segment baked into the key string")
	scopes := fs.String("scopes", "", "comma-separated scopes to grant")
	rateSpec := fs.String("rate", "", "rate limit, e.g. 100/1m (default unlimited)")
	burst := fs.Float64("burst", 0, "bucket capacity (default: the rate's request count)")
	expires := fs.String("expires", "", "expiry, RFC 3339 or YYYY-MM-DD")
	format := fs.String("format", "text", "output: text or json")
	quiet := fs.Bool("quiet", false, "print only the key, for scripts")
	meta := metaFlag{}
	fs.Var(meta, "meta", "metadata k=v (repeatable)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	st, ok := a.openStore(a.storePath(*storePath))
	if !ok {
		return ExitRuntime
	}

	var issued issue.Issued
	// Regenerate on the (astronomically unlikely) ID collision instead
	// of silently replacing an existing record.
	for attempt := 0; ; attempt++ {
		var err error
		issued, err = issue.Issue(issue.Params{
			Name:    *name,
			Label:   *label,
			Scopes:  splitScopes(*scopes),
			Rate:    *rateSpec,
			Burst:   *burst,
			Expires: *expires,
			Meta:    meta,
		}, a.now(), a.Rand)
		if err != nil {
			return a.usageErr(err)
		}
		if _, err := st.Get(issued.Record.ID); errors.Is(err, store.ErrNotFound) {
			break
		}
		if attempt >= 4 {
			return a.runtimeErr(fmt.Errorf("could not generate a fresh key id"))
		}
	}
	st.Put(issued.Record)
	if err := st.Save(); err != nil {
		return a.runtimeErr(err)
	}

	switch {
	case *quiet:
		fmt.Fprintln(a.Stdout, issued.Key.Full)
	case *format == "json":
		a.printJSON(map[string]any{
			"key":    issued.Key.Full,
			"record": server.View(issued.Record, a.now()),
		})
	case *format == "text":
		v := server.View(issued.Record, a.now())
		fmt.Fprintf(a.Stdout, "key:     %s\n", issued.Key.Full)
		fmt.Fprintf(a.Stdout, "id:      %s\n", v.ID)
		fmt.Fprintf(a.Stdout, "name:    %s\n", v.Name)
		fmt.Fprintf(a.Stdout, "scopes:  %s\n", orDash(strings.Join(v.Scopes, ", ")))
		fmt.Fprintf(a.Stdout, "limit:   %s\n", v.Limit)
		fmt.Fprintf(a.Stdout, "expires: %s\n", expiryString(v.ExpiresAt))
		fmt.Fprintln(a.Stderr, "save this key now — keyturn stores only its hash and cannot show it again")
	default:
		return a.usageErr(fmt.Errorf("unknown format %q", *format))
	}
	return ExitOK
}

func (a *App) cmdList(args []string) int {
	fs, storePath := a.newFlagSet("list")
	format := fs.String("format", "text", "output: text or json")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	st, ok := a.openStore(a.storePath(*storePath))
	if !ok {
		return ExitRuntime
	}
	recs := st.List()
	if *format == "json" {
		views := make([]server.RecordView, len(recs))
		for i, r := range recs {
			views[i] = server.View(r, a.now())
		}
		a.printJSON(map[string]any{"keys": views})
		return ExitOK
	}
	if *format != "text" {
		return a.usageErr(fmt.Errorf("unknown format %q", *format))
	}
	if len(recs) == 0 {
		fmt.Fprintln(a.Stdout, "no keys — mint one with: keyturn create --name NAME")
		return ExitOK
	}
	fmt.Fprintf(a.Stdout, "%-12s %-20s %-28s %-16s %s\n", "ID", "NAME", "SCOPES", "LIMIT", "STATUS")
	for _, r := range recs {
		fmt.Fprintf(a.Stdout, "%-12s %-20s %-28s %-16s %s\n",
			r.ID, truncate(r.Name, 20), truncate(orDash(strings.Join(r.Scopes, ",")), 28),
			r.Limit.String(), a.status(r))
	}
	return ExitOK
}

func (a *App) cmdShow(args []string) int {
	fs, storePath := a.newFlagSet("show")
	format := fs.String("format", "text", "output: text or json")
	pos, err := parseMixed(fs, args)
	if err != nil {
		return ExitUsage
	}
	if len(pos) != 1 {
		return a.usageErr(fmt.Errorf("show wants exactly one key ID"))
	}
	st, ok := a.openStore(a.storePath(*storePath))
	if !ok {
		return ExitRuntime
	}
	rec, err := st.Get(pos[0])
	if err != nil {
		return a.runtimeErr(err)
	}
	v := server.View(rec, a.now())
	if *format == "json" {
		a.printJSON(v)
		return ExitOK
	}
	fmt.Fprintf(a.Stdout, "id:        %s\n", v.ID)
	fmt.Fprintf(a.Stdout, "name:      %s\n", v.Name)
	if v.Label != "" {
		fmt.Fprintf(a.Stdout, "label:     %s\n", v.Label)
	}
	fmt.Fprintf(a.Stdout, "scopes:    %s\n", orDash(strings.Join(v.Scopes, ", ")))
	fmt.Fprintf(a.Stdout, "limit:     %s\n", v.Limit)
	fmt.Fprintf(a.Stdout, "remaining: %s\n", remainingString(v.Remaining))
	fmt.Fprintf(a.Stdout, "created:   %s\n", v.CreatedAt.Format(time.RFC3339))
	fmt.Fprintf(a.Stdout, "expires:   %s\n", expiryString(v.ExpiresAt))
	fmt.Fprintf(a.Stdout, "status:    %s\n", a.status(rec))
	if len(v.Meta) > 0 {
		keys := make([]string, 0, len(v.Meta))
		for k := range v.Meta {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(a.Stdout, "meta:      %s=%s\n", k, v.Meta[k])
		}
	}
	return ExitOK
}

func (a *App) cmdSetDisabled(args []string, disabled bool) int {
	verb := "enable"
	if disabled {
		verb = "revoke"
	}
	fs, storePath := a.newFlagSet(verb)
	pos, err := parseMixed(fs, args)
	if err != nil {
		return ExitUsage
	}
	if len(pos) != 1 {
		return a.usageErr(fmt.Errorf("%s wants exactly one key ID", verb))
	}
	st, ok := a.openStore(a.storePath(*storePath))
	if !ok {
		return ExitRuntime
	}
	if err := st.SetDisabled(pos[0], disabled); err != nil {
		return a.runtimeErr(err)
	}
	if err := st.Save(); err != nil {
		return a.runtimeErr(err)
	}
	fmt.Fprintf(a.Stdout, "%sd %s\n", verb, pos[0])
	return ExitOK
}

func (a *App) cmdDelete(args []string) int {
	fs, storePath := a.newFlagSet("delete")
	pos, err := parseMixed(fs, args)
	if err != nil {
		return ExitUsage
	}
	if len(pos) != 1 {
		return a.usageErr(fmt.Errorf("delete wants exactly one key ID"))
	}
	st, ok := a.openStore(a.storePath(*storePath))
	if !ok {
		return ExitRuntime
	}
	if err := st.Delete(pos[0]); err != nil {
		return a.runtimeErr(err)
	}
	if err := st.Save(); err != nil {
		return a.runtimeErr(err)
	}
	fmt.Fprintf(a.Stdout, "deleted %s\n", pos[0])
	return ExitOK
}

func (a *App) cmdVerify(args []string) int {
	fs, storePath := a.newFlagSet("verify")
	scopes := fs.String("scopes", "", "comma-separated scopes to demand")
	cost := fs.Float64("cost", 1, "tokens this call spends")
	format := fs.String("format", "text", "output: text or json")
	pos, err := parseMixed(fs, args)
	if err != nil {
		return ExitUsage
	}
	if len(pos) != 1 {
		return a.usageErr(fmt.Errorf("verify wants exactly one KEY argument"))
	}
	st, ok := a.openStore(a.storePath(*storePath))
	if !ok {
		return ExitRuntime
	}
	res := verify.Check(st, verify.Request{
		Key:    pos[0],
		Scopes: splitScopes(*scopes),
		Cost:   *cost,
	}, a.now())
	// Persist the spent tokens so offline verification actually gates:
	// the bucket state lives in the store file between invocations.
	if res.KeyID != "" {
		if err := st.Save(); err != nil {
			return a.runtimeErr(err)
		}
	}

	switch *format {
	case "json":
		a.printJSON(server.ToResponse(res))
	case "text":
		if res.Valid {
			fmt.Fprintf(a.Stdout, "valid: %s (%s)\n", res.Name, res.KeyID)
			fmt.Fprintf(a.Stdout, "scopes:    %s\n", orDash(strings.Join(res.Scopes, ", ")))
			fmt.Fprintf(a.Stdout, "remaining: %s\n", remainingString(res.Remaining))
		} else {
			fmt.Fprintf(a.Stdout, "denied: %s\n", res.Reason)
			if len(res.MissingScopes) > 0 {
				fmt.Fprintf(a.Stdout, "missing:   %s\n", strings.Join(res.MissingScopes, ", "))
			}
			if res.Reason == verify.ReasonRateLimited && res.RetryAfter >= 0 {
				fmt.Fprintf(a.Stdout, "retry in:  %s\n", res.RetryAfter.Round(time.Millisecond))
			}
		}
	default:
		return a.usageErr(fmt.Errorf("unknown format %q", *format))
	}
	if !res.Valid {
		return ExitDenied
	}
	return ExitOK
}

// ---- small render helpers ----

func (a *App) printJSON(v any) {
	enc := json.NewEncoder(a.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func (a *App) status(r store.Record) string {
	switch {
	case r.Disabled:
		return "revoked"
	case r.ExpiresAt != nil && !a.now().Before(*r.ExpiresAt):
		return "expired"
	default:
		return "active"
	}
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func expiryString(t *time.Time) string {
	if t == nil {
		return "never"
	}
	return t.Format(time.RFC3339)
}

func remainingString(n int64) string {
	if n < 0 {
		return "unlimited"
	}
	return fmt.Sprintf("%d", n)
}
