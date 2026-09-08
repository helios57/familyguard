package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/helios57/familyguard/backend/internal/fgclient"
	"github.com/helios57/familyguard/backend/internal/store"
	"golang.org/x/term"
)

// commandAliases maps the friendly verbs onto the server's closed command set.
//
// The values are the store constants rather than string literals, so a command type renamed on the
// server breaks the build here instead of producing a 400 at the moment a parent needs the siren.
var commandAliases = map[string]string{
	"ring":      store.CmdTypeTriggerAlarm,
	"stop-ring": store.CmdTypeStopAlarm,
	"lock":      store.CmdTypeLockNow,
	"unlock":    store.CmdTypeUnlockDevice,
	"locate":    store.CmdTypeLocateNow,
	"sync":      store.CmdTypeSyncPolicy,
	"update":    store.CmdTypeUpdateApp,
}

func commands() []command {
	list := []command{
		{"login", "--url <base-url> [--token-stdin]", "store an API key for this server", false, cmdLogin},
		{"logout", "", "forget the stored credential", false, cmdLogout},
		{"config", "", "show the server and whether a credential is stored", false, cmdConfig},
		{"version", "", "print the fgctl version", false, cmdVersion},
		{"whoami", "", "who the stored credential acts as", true, cmdWhoami},
		{"children", "", "list the children in the family", true, cmdChildren},
		{"devices", "", "list every enrolled device", true, cmdDevices},
		{"device", "<device-id>", "one device with its reported state", true, cmdDevice},
		{"policy", "<child-id>", "the policy in force for a child", true, cmdPolicy},
		{"commands", "<device-id> [--limit n]", "the command queue and its timings", true, cmdCommands},
		{"send", "<device-id> <TYPE>", "queue any command in the server's set", true, cmdSend},
		{"apps", "", "the APKs this deployment hosts", true, cmdApps},
		{"blocklist", "", "the family-wide blocked packages", true, cmdBlocklist},
		{"usage", "<device-id>", "reported screen time", true, cmdUsage},
		{"locations", "<device-id>", "reported locations", true, cmdLocations},
		{"audit", "[--limit n]", "the audit log, newest first", true, cmdAudit},
		{"keys", "", "the API keys of this family", true, cmdKeys},
		{"rm-device", "<device-id> --yes", "delete a device (not exposed over MCP)", true, cmdRemoveDevice},
		{"rm-child", "<child-id> --yes", "delete a child and its devices (not exposed over MCP)", true, cmdRemoveChild},
		{"mcp", "", "serve these operations over MCP on stdio", true, cmdMCP},
	}
	// The verbs are the commands people actually reach for, so they are listed, not hidden behind
	// `send`. Generated from the same map that implements them: a verb cannot exist in help and be
	// unimplemented, nor the reverse.
	for _, verb := range sortedKeys(commandAliases) {
		list = append(list, command{
			name:      verb,
			args:      "<device-id>",
			summary:   "queue " + commandAliases[verb],
			needsAuth: true,
			run:       cmdSend,
		})
	}
	return list
}

// ---- credential commands --------------------------------------------------

func cmdLogin(ctx context.Context, env *environment, args []string) error {
	baseURL := env.cfg.BaseURL
	fromStdin := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--url":
			if i+1 >= len(args) {
				return fmt.Errorf("--url needs a value, e.g. --url https://guard.example.com")
			}
			baseURL = strings.TrimRight(args[i+1], "/")
			i++
		case "--token-stdin":
			fromStdin = true
		default:
			return fmt.Errorf("unexpected argument %q", args[i])
		}
	}
	if baseURL == "" {
		return fgclient.ErrNoServer
	}
	if !strings.HasPrefix(baseURL, "https://") && !strings.HasPrefix(baseURL, "http://") {
		return fmt.Errorf("--url must include the scheme, e.g. https://guard.example.com")
	}

	token, err := readToken(fromStdin)
	if err != nil {
		return err
	}
	if token == "" {
		return fmt.Errorf("no key given")
	}

	// Verify BEFORE storing. A login that writes an unverified credential and reports success is
	// the false green this project keeps finding: the failure then surfaces later, on an unrelated
	// command, and reads as a server problem.
	probe := fgclient.New(baseURL, token)
	var parent store.Parent
	if err := probe.Get(ctx, "/api/v1/me", &parent); err != nil {
		return fmt.Errorf("the server did not accept that key: %w", err)
	}

	path, err := saveConfig(config{BaseURL: baseURL, Token: token})
	if err != nil {
		return err
	}
	fmt.Fprintf(env.out, "Signed in to %s as %s (%s), role %s.\nCredential stored in %s.\n",
		baseURL, firstNonEmpty(parent.DisplayName, parent.Email), parent.Email, parent.Role, path)
	return nil
}

// readToken takes the key from a terminal without echoing it, or from stdin when piped.
//
// term.ReadPassword is what makes this work identically on Windows: the console there has no tty
// to put in raw mode, and the package handles that difference rather than this code.
func readToken(fromStdin bool) (string, error) {
	if fromStdin || !term.IsTerminal(int(os.Stdin.Fd())) {
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return "", fmt.Errorf("reading the key from stdin: %w", err)
			}
			return "", fmt.Errorf("no key on stdin")
		}
		return strings.TrimSpace(scanner.Text()), nil
	}
	// The prompt goes to stderr so `fgctl login` can be used in a pipeline without the prompt
	// landing in the captured output.
	fmt.Fprint(os.Stderr, "API key (fgk_…, not echoed): ")
	raw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("reading the key: %w", err)
	}
	return strings.TrimSpace(string(raw)), nil
}

func cmdLogout(_ context.Context, env *environment, _ []string) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing %s: %w", path, err)
	}
	fmt.Fprintf(env.out, "Removed %s.\n", path)
	if os.Getenv("FAMILYGUARD_TOKEN") != "" {
		// Otherwise the next command still works and the user concludes logout did nothing.
		fmt.Fprintln(env.out, "Note: FAMILYGUARD_TOKEN is still set in this environment and overrides the file.")
	}
	return nil
}

func cmdConfig(_ context.Context, env *environment, _ []string) error {
	path, err := configPath()
	if err != nil {
		return err
	}
	type view struct {
		Path             string `json:"path"`
		BaseURL          string `json:"base_url"`
		CredentialStored bool   `json:"credential_stored"`
		FromEnvironment  bool   `json:"from_environment"`
	}
	// The token itself is never rendered, in either mode. Not even a prefix: a prefix identifies a
	// key in a leaked recording and answers no question that "stored" does not.
	v := view{
		Path:             path,
		BaseURL:          env.cfg.BaseURL,
		CredentialStored: env.cfg.Token != "",
		FromEnvironment:  os.Getenv("FAMILYGUARD_TOKEN") != "" || os.Getenv("FAMILYGUARD_URL") != "",
	}
	return env.emit(v, func(w *tabwriter.Writer) {
		fmt.Fprintf(w, "config file\t%s\n", v.Path)
		fmt.Fprintf(w, "server\t%s\n", firstNonEmpty(v.BaseURL, "(none — run `fgctl login --url …`)"))
		fmt.Fprintf(w, "credential\t%s\n", map[bool]string{true: "stored", false: "none"}[v.CredentialStored])
		if v.FromEnvironment {
			fmt.Fprintf(w, "environment\tFAMILYGUARD_URL/FAMILYGUARD_TOKEN are set and override the file\n")
		}
	})
}

func cmdVersion(_ context.Context, env *environment, _ []string) error {
	fmt.Fprintf(env.out, "fgctl %s\n", version)
	return nil
}

func cmdWhoami(ctx context.Context, env *environment, _ []string) error {
	var parent store.Parent
	if err := env.client.Get(ctx, "/api/v1/me", &parent); err != nil {
		return err
	}
	return env.emit(parent, func(w *tabwriter.Writer) {
		fmt.Fprintf(w, "name\t%s\n", parent.DisplayName)
		fmt.Fprintf(w, "email\t%s\n", parent.Email)
		fmt.Fprintf(w, "role\t%s\n", parent.Role)
		fmt.Fprintf(w, "id\t%s\n", parent.ID)
	})
}

// ---- read commands --------------------------------------------------------

func cmdChildren(ctx context.Context, env *environment, _ []string) error {
	var body struct {
		Children []store.Child `json:"children"`
	}
	if err := env.client.Get(ctx, "/api/v1/children", &body); err != nil {
		return err
	}
	return env.emit(body.Children, func(w *tabwriter.Writer) {
		if len(body.Children) == 0 {
			fmt.Fprintln(w, "no children")
			return
		}
		fmt.Fprintln(w, "ID\tNAME\tBIRTH YEAR")
		for _, c := range body.Children {
			year := "—"
			if c.BirthYear != nil {
				year = strconv.Itoa(*c.BirthYear)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\n", c.ID, c.Name, year)
		}
	})
}

func cmdDevices(ctx context.Context, env *environment, _ []string) error {
	var body struct {
		Devices []store.Device `json:"devices"`
	}
	if err := env.client.Get(ctx, "/api/v1/devices", &body); err != nil {
		return err
	}
	return env.emit(body.Devices, func(w *tabwriter.Writer) {
		if len(body.Devices) == 0 {
			fmt.Fprintln(w, "no devices")
			return
		}
		fmt.Fprintln(w, "ID\tNAME\tMODEL\tOS\tLOCKED\tENROLLED")
		for _, d := range body.Devices {
			enrolled := "no"
			if d.EnrolledAt != nil {
				enrolled = d.EnrolledAt.UTC().Format("2006-01-02")
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%v\t%s\n", d.ID, d.Name, d.Model, d.OSVersion, d.Locked, enrolled)
		}
	})
}

// deviceView is the shape GET /devices/:id returns. state is a pointer because a device that has
// never checked in has no state row, and the server reports that as absent rather than zeroed --
// "battery 0%, offline" would be a measurement nobody took.
type deviceView struct {
	Device   store.Device       `json:"device"`
	State    *store.DeviceState `json:"state"`
	Enrolled bool               `json:"enrolled"`
}

func fetchDevice(ctx context.Context, client *fgclient.Client, id string) (deviceView, error) {
	var view deviceView
	err := client.Get(ctx, "/api/v1/devices/"+id, &view)
	return view, err
}

func cmdDevice(ctx context.Context, env *environment, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: fgctl device <device-id>")
	}
	view, err := fetchDevice(ctx, env.client, args[0])
	if err != nil {
		return err
	}
	return env.emit(view, func(w *tabwriter.Writer) {
		d := view.Device
		fmt.Fprintf(w, "name\t%s\n", d.Name)
		fmt.Fprintf(w, "id\t%s\n", d.ID)
		fmt.Fprintf(w, "model\t%s (%s)\n", d.Model, d.OSVersion)
		fmt.Fprintf(w, "locked\t%v\n", d.Locked)
		fmt.Fprintf(w, "enrolled\t%v\n", view.Enrolled)
		if view.State == nil {
			fmt.Fprintln(w, "state\tthis device has never checked in — nothing measured")
			return
		}
		s := view.State
		fmt.Fprintf(w, "online\t%v\n", s.Online)
		fmt.Fprintf(w, "last seen\t%s\n", ago(s.LastSeenAt))
		battery := "not reported"
		if s.BatteryLevel != nil {
			battery = strconv.Itoa(*s.BatteryLevel) + "%"
		}
		fmt.Fprintf(w, "battery\t%s (charging: %s)\n", battery, tri(s.Charging))
		fmt.Fprintf(w, "screen on\t%s\n", tri(s.ScreenOn))
		fmt.Fprintf(w, "connectivity\t%s\n", firstNonEmpty(s.Connectivity, "not reported"))
		fmt.Fprintf(w, "DPC build\t%s (%d)\n", firstNonEmpty(s.AppVersionName, "?"), s.AppVersionCode)
		fmt.Fprintf(w, "policy version\t%d\n", s.PolicyVersion)
		fmt.Fprintf(w, "usage access\t%s\n", tri(s.UsageAccess))
		fmt.Fprintf(w, "battery unrestricted\t%s\n", tri(s.PowerExempt))
		fmt.Fprintf(w, "exact alarms\t%s\n", tri(s.ExactAlarms))
		if s.UpdateError != "" {
			fmt.Fprintf(w, "update error\t%s (%s)\n", s.UpdateError, ago(s.UpdateErrorAt))
		}
		// The remedy, printed only when the phone has actually said the switch is off. A false here
		// is a measured finding; a nil is the phone not having reported, and prompting on a nil
		// would send a parent into Settings on no evidence.
		if (s.PowerExempt != nil && !*s.PowerExempt) || (s.ExactAlarms != nil && !*s.ExactAlarms) {
			fmt.Fprintln(w, "\t")
			fmt.Fprintln(w, "WARNING\tthis phone is delaying FamilyGuard in the background.")
			fmt.Fprintln(w, "\tRing, Lock and Locate can take minutes to arrive while it is asleep,")
			fmt.Fprintln(w, "\tand nothing reports an error when they do — the work is late, not lost.")
			if s.PowerExempt != nil && !*s.PowerExempt {
				fmt.Fprintln(w, "\t• Settings → Apps → FamilyGuard → Battery → Unrestricted")
				fmt.Fprintln(w, "\t  (Samsung: also Settings → Battery → Background usage limits)")
			}
			if s.ExactAlarms != nil && !*s.ExactAlarms {
				fmt.Fprintln(w, "\t• Settings → Apps → FamilyGuard → Alarms and reminders → allow")
			}
		}
	})
}

func cmdPolicy(ctx context.Context, env *environment, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: fgctl policy <child-id>")
	}
	var pol store.Policy
	if err := env.client.Get(ctx, "/api/v1/children/"+args[0]+"/policy", &pol); err != nil {
		return err
	}
	return env.emit(pol, func(w *tabwriter.Writer) {
		fmt.Fprintf(w, "child\t%s\n", pol.ChildID)
		fmt.Fprintf(w, "version\t%d (updated %s)\n", pol.Version, ago(&pol.UpdatedAt))
		fmt.Fprintf(w, "tracking only\t%v\n", pol.TrackingOnly)
		fmt.Fprintf(w, "daily limit\t%d minutes\n", pol.DailyLimitMinutes)
		bedtime := "off"
		if pol.BedtimeEnabled {
			bedtime = pol.BedtimeStart + " – " + pol.BedtimeEnd
		}
		fmt.Fprintf(w, "bedtime\t%s\n", bedtime)
		fmt.Fprintf(w, "timezone\t%s\n", pol.Timezone)
		fmt.Fprintf(w, "YouTube blocked\t%v\n", pol.YouTubeBlocked)
		fmt.Fprintf(w, "child installs\t%v\n", pol.AllowChildInstalls)
		fmt.Fprintf(w, "debugging allowed\t%v\n", pol.AllowDebugging)
		fmt.Fprintf(w, "DNS host\t%s\n", firstNonEmpty(pol.DNSHost, "(none)"))
	})
}

func cmdCommands(ctx context.Context, env *environment, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: fgctl commands <device-id> [--limit n]")
	}
	limit := flagValue(args[1:], "--limit")
	var body struct {
		Commands []store.Command `json:"commands"`
	}
	path := fgclient.Query("/api/v1/devices/"+args[0]+"/commands", map[string]string{"limit": limit})
	if err := env.client.Get(ctx, path, &body); err != nil {
		return err
	}
	return env.emit(body.Commands, func(w *tabwriter.Writer) {
		if len(body.Commands) == 0 {
			fmt.Fprintln(w, "no commands")
			return
		}
		// The two latencies are the point of this table. delivered-created is how long the phone
		// took to come and ask; acked-delivered is how long it then took to act. A battery-
		// restricted phone shows a large first number and a tiny second one, which is what
		// distinguishes "the phone was asleep" from "the phone is broken".
		fmt.Fprintln(w, "CREATED\tTYPE\tSTATE\tTO DELIVER\tTO ACK\tERROR")
		for _, c := range body.Commands {
			toDeliver, toAck := "—", "—"
			if c.DeliveredAt != nil {
				toDeliver = c.DeliveredAt.Sub(c.CreatedAt).Round(time.Millisecond).String()
				if c.AckedAt != nil {
					toAck = c.AckedAt.Sub(*c.DeliveredAt).Round(time.Millisecond).String()
				}
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				c.CreatedAt.UTC().Format("2006-01-02 15:04:05"),
				c.Type, c.State, toDeliver, toAck, truncate(c.Error, 40))
		}
	})
}

func cmdApps(ctx context.Context, env *environment, _ []string) error {
	var body struct {
		Apps       []map[string]any `json:"apps"`
		Configured bool             `json:"configured"`
	}
	if err := env.client.Get(ctx, "/api/v1/apps", &body); err != nil {
		return err
	}
	return env.emit(body, func(w *tabwriter.Writer) {
		if !body.Configured {
			fmt.Fprintln(w, "the app catalogue is not configured on this deployment")
		}
		if len(body.Apps) == 0 {
			fmt.Fprintln(w, "no apps")
			return
		}
		fmt.Fprintln(w, "PACKAGE\tLABEL\tVERSION")
		for _, a := range body.Apps {
			fmt.Fprintf(w, "%v\t%v\t%v\n", a["package_name"], a["label"], a["version_name"])
		}
	})
}

func cmdBlocklist(ctx context.Context, env *environment, _ []string) error {
	var body struct {
		Packages []map[string]any `json:"packages"`
	}
	if err := env.client.Get(ctx, "/api/v1/family/blocked-packages", &body); err != nil {
		return err
	}
	return env.emit(body.Packages, func(w *tabwriter.Writer) {
		if len(body.Packages) == 0 {
			fmt.Fprintln(w, "no blocked packages")
			return
		}
		fmt.Fprintln(w, "PACKAGE\tSOURCE")
		for _, p := range body.Packages {
			fmt.Fprintf(w, "%v\t%v\n", p["package_name"], p["source"])
		}
	})
}

func cmdUsage(ctx context.Context, env *environment, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: fgctl usage <device-id>")
	}
	var body map[string]any
	if err := env.client.Get(ctx, "/api/v1/devices/"+args[0]+"/usage", &body); err != nil {
		return err
	}
	return env.emit(body, func(w *tabwriter.Writer) {
		if len(body) == 0 {
			fmt.Fprintln(w, "nothing reported")
			return
		}
		for _, k := range sortedKeys(body) {
			fmt.Fprintf(w, "%s\t%v\n", k, body[k])
		}
	})
}

func cmdLocations(ctx context.Context, env *environment, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: fgctl locations <device-id>")
	}
	var body struct {
		Locations []map[string]any `json:"locations"`
	}
	if err := env.client.Get(ctx, "/api/v1/devices/"+args[0]+"/locations", &body); err != nil {
		return err
	}
	return env.emit(body.Locations, func(w *tabwriter.Writer) {
		if len(body.Locations) == 0 {
			fmt.Fprintln(w, "no locations reported")
			return
		}
		fmt.Fprintln(w, "AT\tLATITUDE\tLONGITUDE\tACCURACY")
		for _, l := range body.Locations {
			fmt.Fprintf(w, "%v\t%v\t%v\t%v\n", l["recorded_at"], l["latitude"], l["longitude"], l["accuracy_m"])
		}
	})
}

func cmdAudit(ctx context.Context, env *environment, args []string) error {
	var body struct {
		Entries []store.AuditEntry `json:"entries"`
	}
	path := fgclient.Query("/api/v1/audit", map[string]string{"limit": flagValue(args, "--limit")})
	if err := env.client.Get(ctx, path, &body); err != nil {
		return err
	}
	return env.emit(body.Entries, func(w *tabwriter.Writer) {
		if len(body.Entries) == 0 {
			fmt.Fprintln(w, "no audit entries")
			return
		}
		fmt.Fprintln(w, "WHEN\tACTOR\tACTION\tTARGET")
		for _, e := range body.Entries {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s %s\n",
				e.OccurredAt.UTC().Format("2006-01-02 15:04:05"),
				e.ActorType, e.Action, e.TargetType, truncate(e.TargetID, 12))
		}
	})
}

func cmdKeys(ctx context.Context, env *environment, _ []string) error {
	var body struct {
		APIKeys []store.APIKey `json:"api_keys"`
	}
	if err := env.client.Get(ctx, "/api/v1/api-keys", &body); err != nil {
		return err
	}
	return env.emit(body.APIKeys, func(w *tabwriter.Writer) {
		if len(body.APIKeys) == 0 {
			fmt.Fprintln(w, "no API keys")
			return
		}
		// Prefix is shown because the server stores and returns it as an identifier -- it is not
		// the secret, and "which of these five keys is the one on my laptop" has no other answer.
		fmt.Fprintln(w, "NAME\tPREFIX\tLAST USED\tREVOKED")
		for _, k := range body.APIKeys {
			revoked := "no"
			if k.RevokedAt != nil {
				revoked = k.RevokedAt.UTC().Format("2006-01-02")
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", k.Name, k.Prefix, ago(k.LastUsedAt), revoked)
		}
	})
}

// ---- write commands -------------------------------------------------------

func cmdSend(ctx context.Context, env *environment, args []string) error {
	// Reached both as `fgctl send <id> <TYPE>` and as `fgctl ring <id>`. Dispatch records which
	// name was used; the alias table decides which of the two shapes applies.
	verb := env.verb
	var deviceID, commandType string
	if mapped, isAlias := commandAliases[verb]; isAlias {
		if len(args) < 1 {
			return fmt.Errorf("usage: fgctl %s <device-id>", verb)
		}
		deviceID, commandType = args[0], mapped
	} else {
		if len(args) < 2 {
			return fmt.Errorf("usage: fgctl send <device-id> <TYPE>  (one of: %s)",
				strings.Join(sortedKeys(store.ValidCommandTypes), ", "))
		}
		deviceID, commandType = args[0], strings.ToUpper(args[1])
	}
	// Validated here against the server's own set, so a typo costs nothing and the error names the
	// alternatives. The server validates too; this is for the message, not for safety.
	if !store.ValidCommandTypes[commandType] {
		return fmt.Errorf("%q is not a command this server accepts (one of: %s)",
			commandType, strings.Join(sortedKeys(store.ValidCommandTypes), ", "))
	}
	var created store.Command
	body := map[string]any{"type": commandType}
	if err := env.client.Do(ctx, "POST", "/api/v1/devices/"+deviceID+"/commands", body, &created); err != nil {
		return err
	}
	return env.emit(created, func(w *tabwriter.Writer) {
		fmt.Fprintf(w, "queued\t%s\n", created.Type)
		fmt.Fprintf(w, "command id\t%s\n", created.ID)
		fmt.Fprintf(w, "state\t%s\n", created.State)
		fmt.Fprintf(w, "expires\t%s\n", created.ExpiresAt.UTC().Format(time.RFC3339))
		fmt.Fprintln(w, "\t")
		fmt.Fprintf(w, "\tqueued, not delivered. Run `fgctl commands %s` to see when the phone took it.\n", deviceID)
	})
}

// cmdRemoveDevice and cmdRemoveChild are deliberately CLI-only and are not registered as MCP tools.
// An API key is the parent that made it, so the capability exists either way; the question is only
// whether a model driving this binary can reach it in a single step, and the answer here is no.
func cmdRemoveDevice(ctx context.Context, env *environment, args []string) error {
	return removeThing(ctx, env, args, "device", "/api/v1/devices/")
}

func cmdRemoveChild(ctx context.Context, env *environment, args []string) error {
	return removeThing(ctx, env, args, "child", "/api/v1/children/")
}

func removeThing(ctx context.Context, env *environment, args []string, noun, prefix string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: fgctl rm-%s <%s-id> --yes", noun, noun)
	}
	if !hasFlag(args, "--yes") {
		return fmt.Errorf("refusing to delete %s %s without --yes", noun, args[0])
	}
	if err := env.client.Do(ctx, "DELETE", prefix+args[0], nil, nil); err != nil {
		return err
	}
	fmt.Fprintf(env.out, "Deleted %s %s.\n", noun, args[0])
	return nil
}

// ---- small argument helpers ----------------------------------------------

func flagValue(args []string, name string) string {
	for i, a := range args {
		if a == name && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, name+"=") {
			return strings.TrimPrefix(a, name+"=")
		}
	}
	return ""
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}
