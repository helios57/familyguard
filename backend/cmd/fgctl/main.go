// Command fgctl is the FamilyGuard command-line client, and the same binary serves it over MCP.
//
// It authenticates with an API key (FR-17) -- `fgctl login` -- so everything a parent can do in the
// console can be done from a script, and `fgctl mcp` exposes the same operations to an MCP client
// without a second implementation of anything.
//
// There is no compiled-in server address. This repository is public, so a default host would put a
// real deployment's address in published source; an unconfigured binary says how to configure
// itself instead.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/helios57/familyguard/backend/internal/fgclient"
)

// version is stamped at build time with -ldflags "-X main.version=…". It is reported to MCP clients
// and by `fgctl version`, so a stale binary in a PATH is visible rather than guessed at.
var version = "dev"

type command struct {
	name    string
	args    string
	summary string
	// needsAuth is false only for the handful of commands that must work before a credential
	// exists, or the CLI cannot bootstrap itself.
	needsAuth bool
	run       func(ctx context.Context, env *environment, args []string) error
}

// environment is what a command is given: a configured client, or a reason there is not one.
type environment struct {
	cfg    config
	client *fgclient.Client
	json   bool
	out    *os.File
	// verb is the name this command was invoked as. The alias verbs (ring, lock, …) all run the
	// same function and need to know which one they were, and reading os.Args from inside a
	// command couples it to how dispatch happens to work.
	verb string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		// Interrupt is how a user stops a long list; it is not a failure to report as one.
		if errors.Is(err, context.Canceled) {
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, "fgctl: "+err.Error())
		var apiErr *fgclient.APIError
		if errors.As(err, &apiErr) && apiErr.Unauthorized() {
			fmt.Fprintln(os.Stderr, "  the credential was refused — run `fgctl login` to replace it")
		}
		if errors.Is(err, fgclient.ErrNoCredential) || errors.Is(err, fgclient.ErrNoServer) {
			os.Exit(3)
		}
		if errors.Is(err, errUsage) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

// errUsage is "you did not say what to do", which is exit 2 -- distinct from 1 (the command ran
// and failed) and from 3 (no credential), so a script can tell the three apart.
var errUsage = errors.New("no command given")

func run(ctx context.Context, argv []string) error {
	// --json is stripped before the subcommand is chosen, so it works in front of it as well as
	// after. Doing this after picking the command made `fgctl --json children` fail with
	// "unknown command --json", which blames the wrong thing.
	env := &environment{out: os.Stdout}
	positional := make([]string, 0, len(argv))
	for _, arg := range argv {
		if arg == "--json" {
			env.json = true
			continue
		}
		positional = append(positional, arg)
	}
	if len(positional) == 0 {
		// Not the same as asking for help: `fgctl $CMD` with an unset variable would otherwise
		// print the usage text and exit 0, reporting success for having done nothing.
		usage()
		return errUsage
	}
	if positional[0] == "help" || positional[0] == "-h" || positional[0] == "--help" {
		// Asking for help and getting it is a success.
		usage()
		return nil
	}
	name := positional[0]
	rest := positional[1:]
	env.verb = name

	cmd, ok := lookup(name)
	if !ok {
		usage()
		return fmt.Errorf("unknown command %q", name)
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	env.cfg = cfg
	env.client = fgclient.New(cfg.BaseURL, cfg.Token)
	if cmd.needsAuth {
		if cfg.BaseURL == "" {
			return fgclient.ErrNoServer
		}
		if cfg.Token == "" {
			return fgclient.ErrNoCredential
		}
	}
	return cmd.run(ctx, env, rest)
}

func lookup(name string) (command, bool) {
	for _, c := range commands() {
		if c.name == name {
			return c, true
		}
	}
	return command{}, false
}

func usage() {
	fmt.Fprintf(os.Stderr, "fgctl %s — FamilyGuard from the command line, and over MCP.\n\n", version)
	fmt.Fprintln(os.Stderr, "Usage: fgctl <command> [arguments] [--json]")
	fmt.Fprintln(os.Stderr)
	w := tabwriter.NewWriter(os.Stderr, 0, 0, 2, ' ', 0)
	for _, c := range commands() {
		invocation := c.name
		if c.args != "" {
			invocation += " " + c.args
		}
		fmt.Fprintf(w, "  %s\t%s\n", invocation, c.summary)
	}
	w.Flush()
	fmt.Fprintln(os.Stderr, "\nEnvironment: FAMILYGUARD_URL and FAMILYGUARD_TOKEN override the saved configuration.")
	fmt.Fprintln(os.Stderr, "Add --json to any command for machine-readable output.")
}

// ---- output helpers -------------------------------------------------------

// emit prints a value as JSON when --json was given, and otherwise runs the human renderer.
//
// Every command goes through this, so --json is never a per-command afterthought that some
// subcommand forgot to honour.
func (e *environment) emit(value any, human func(*tabwriter.Writer)) error {
	if e.json {
		encoder := json.NewEncoder(e.out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	}
	w := tabwriter.NewWriter(e.out, 0, 0, 2, ' ', 0)
	human(w)
	return w.Flush()
}

// tri renders a three-valued boolean the way the whole project treats them: null is "the phone has
// not said", which is not the same as false and must never be shown as one.
func tri(v *bool) string {
	switch {
	case v == nil:
		return "not reported"
	case *v:
		return "yes"
	default:
		return "no"
	}
}

func ago(t *time.Time) string {
	if t == nil {
		return "never"
	}
	d := time.Since(*t).Round(time.Second)
	if d < 0 {
		return t.UTC().Format(time.RFC3339)
	}
	return d.String() + " ago"
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
