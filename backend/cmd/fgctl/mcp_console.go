package main

// The rest of the console over MCP (owner, 2026-10-01: "add the mcp, it has to cover everything the
// console can"). mcp.go holds the tools that came first; these are the ones that until 0.6.36 only a
// browser could reach: the rules a parent sets (policy, apps, domains, the family blocklist), the
// children and phones themselves, the app catalog, and the answer to "Mehr Zeit erbitten".
//
// What is still not a tool is listed, with its reason, in mcpUncovered (mcp_coverage_test.go holds
// every parent route of the server to a tool or to that list): the routes only a signed-in parent may
// call — people and credentials, which an API key must never be able to mint — a browser's own Web
// Push subscription, the event stream, the adb byte stream, and deleting a phone or a child, which
// the console does not offer either and the CLI does behind --yes.

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/helios57/familyguard/backend/internal/fgclient"
	"github.com/helios57/familyguard/backend/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type createChildArgs struct {
	Name      string `json:"name" jsonschema:"the child's name as the family says it"`
	BirthYear *int   `json:"birth_year,omitempty" jsonschema:"optional, such as 2015"`
}

type updateChildArgs struct {
	ChildID   string `json:"child_id" jsonschema:"the child's UUID, as returned by list_children"`
	Name      string `json:"name" jsonschema:"the child's name"`
	BirthYear *int   `json:"birth_year,omitempty" jsonschema:"optional, such as 2015; omit to clear it"`
}

// Every field optional and a pointer: only what is sent changes, exactly as the console's switches do.
type policyArgs struct {
	ChildID            string  `json:"child_id" jsonschema:"the child's UUID, as returned by list_children"`
	DailyLimitMinutes  *int    `json:"daily_limit_minutes,omitempty" jsonschema:"Tageszeit: screen time per day in minutes, 0 to 1440; 0 is no limit. It starts over at midnight"`
	BedtimeEnabled     *bool   `json:"bedtime_enabled,omitempty" jsonschema:"true pauses apps over night between bedtime_start and bedtime_end; calls always work"`
	BedtimeStart       *string `json:"bedtime_start,omitempty" jsonschema:"HH:MM, such as 21:00"`
	BedtimeEnd         *string `json:"bedtime_end,omitempty" jsonschema:"HH:MM, such as 06:30"`
	Timezone           *string `json:"timezone,omitempty" jsonschema:"IANA zone such as Europe/Zurich; bedtime and the daily reset are measured in it"`
	YouTubeBlocked     *bool   `json:"youtube_blocked,omitempty" jsonschema:"true blocks the YouTube apps"`
	AllowChildInstalls *bool   `json:"allow_child_installs,omitempty" jsonschema:"true lets the child install apps without a parent's answer"`
	AllowUninstall     *bool   `json:"allow_uninstall,omitempty" jsonschema:"true lets the child uninstall apps"`
	AllowDebugging     *bool   `json:"allow_debugging,omitempty" jsonschema:"true leaves Android's developer options and USB debugging usable"`
	TrackingOnly       *bool   `json:"tracking_only,omitempty" jsonschema:"true only watches: nothing is paused or blocked, everything is still measured"`
	AdFilter           *bool   `json:"ad_filter,omitempty" jsonschema:"true runs the phone's own ad filter"`
	AdFilterListURL    *string `json:"ad_filter_list_url,omitempty" jsonschema:"the filter list's https address; empty for the built-in default"`
	DNSHost            *string `json:"dns_host,omitempty" jsonschema:"a Private DNS host name, or empty for none"`
}

type answerArgs struct {
	ChildID   string `json:"child_id" jsonschema:"the child's UUID, as returned by list_children"`
	RequestID string `json:"request_id" jsonschema:"the request's id from get_today's time_requests"`
	Decision  string `json:"decision" jsonschema:"grant or decline"`
	Minutes   int    `json:"minutes,omitempty" jsonschema:"grant only: 1 to 240 minutes to give, more or less than asked; omit to give what was asked"`
}

type appRuleArgs struct {
	ChildID      string `json:"child_id" jsonschema:"the child's UUID, as returned by list_children"`
	PackageName  string `json:"package_name" jsonschema:"the Android package, such as com.whatsapp, from list_device_apps"`
	Rule         string `json:"rule" jsonschema:"ALLOW (always usable, never counted), LIMIT (counts against the day; with limit_minutes also its own daily allowance), BONUS (runs only on Bonuszeit), BLOCK (never), or NONE to remove the rule so the app waits for a decision again"`
	LimitMinutes int    `json:"limit_minutes,omitempty" jsonschema:"LIMIT only: the app's own minutes per day, 0 for none beyond the daily limit"`
}

type domainArgs struct {
	ChildID string `json:"child_id" jsonschema:"the child's UUID, as returned by list_children"`
	Domain  string `json:"domain" jsonschema:"a domain such as example.com; its subdomains are included"`
	Blocked bool   `json:"blocked" jsonschema:"true blocks it on the child's phones, false lifts the block"`
}

type familyBlockArgs struct {
	PackageName string `json:"package_name" jsonschema:"the Android package, such as com.example.app"`
	Blocked     bool   `json:"blocked" jsonschema:"true: nobody in the family may have it; false takes it off the list"`
	Label       string `json:"label,omitempty" jsonschema:"the app's name, for the list"`
	Reason      string `json:"reason,omitempty" jsonschema:"why, for the list"`
}

type addDeviceArgs struct {
	ChildID string `json:"child_id" jsonschema:"the child's UUID, as returned by list_children"`
	Name    string `json:"name" jsonschema:"a name the family will recognise, such as Mira's phone"`
}

type renameDeviceArgs struct {
	DeviceID string `json:"device_id" jsonschema:"the device's UUID, as returned by list_devices"`
	Name     string `json:"name" jsonschema:"the new name"`
}

type setupCodeArgs struct {
	DeviceID        string `json:"device_id" jsonschema:"the device's UUID, as returned by list_devices"`
	ReplaceEnrolled bool   `json:"replace_enrolled,omitempty" jsonschema:"must be true for a phone that is already set up: the new code replaces its credential (to re-link it)"`
}

type deviceAppsArgs struct {
	DeviceID      string `json:"device_id" jsonschema:"the device's UUID, as returned by list_devices"`
	IncludeSystem bool   `json:"include_system,omitempty" jsonschema:"true also lists the system's own apps"`
}

type devicePackageArgs struct {
	DeviceID    string `json:"device_id" jsonschema:"the device's UUID, as returned by list_devices"`
	PackageName string `json:"package_name" jsonschema:"the Android package to take off the device's list"`
}

type timelineArgs struct {
	DeviceID string `json:"device_id" jsonschema:"the device's UUID, as returned by list_devices"`
	Day      string `json:"day,omitempty" jsonschema:"YYYY-MM-DD in the child's timezone; default today"`
}

type uploadArgs struct {
	Path  string `json:"path" jsonschema:"the APK file on this computer, an absolute path"`
	Label string `json:"label,omitempty" jsonschema:"optional name for the catalog"`
}

type appIDArgs struct {
	AppID string `json:"app_id" jsonschema:"the catalog entry's id, as returned by list_apps"`
}

type managedArgs struct {
	ChildID     string `json:"child_id" jsonschema:"the child's UUID, as returned by list_children"`
	PackageName string `json:"package_name" jsonschema:"a package from list_apps"`
	Managed     bool   `json:"managed" jsonschema:"true installs it on the child's phones and keeps it current; false removes it from them"`
}

func registerConsoleTools(server *mcp.Server, client *fgclient.Client) {
	// ---- the family -------------------------------------------------------------------------

	add(server, client, "get_family",
		"The family: its name and id.",
		func(ctx context.Context, c *fgclient.Client, _ emptyArgs) (any, error) {
			return getJSON(ctx, c, "/api/v1/family")
		})

	add(server, client, "list_parents",
		"The parents and guardians of the family with their roles (PRIMARY_ADMIN, ADMIN, GUARDIAN). Adding, "+
			"changing or removing a person is only possible signed in to the console, never with an API key.",
		func(ctx context.Context, c *fgclient.Client, _ emptyArgs) (any, error) {
			return getJSON(ctx, c, "/api/v1/parents")
		})

	add(server, client, "list_api_keys",
		"The API keys of the family (primary admin only): name, who made it, when last used, revoked or not. "+
			"Never the secret. Making or revoking a key is only possible signed in to the console.",
		func(ctx context.Context, c *fgclient.Client, _ emptyArgs) (any, error) {
			return getJSON(ctx, c, "/api/v1/api-keys")
		})

	add(server, client, "get_hosted_dpc",
		"The FamilyGuard phone app this server hands out: version, versionCode and checksum. A phone "+
			"reporting an older build in get_device updates with send_command UPDATE_APP.",
		func(ctx context.Context, c *fgclient.Client, _ emptyArgs) (any, error) {
			return getJSON(ctx, c, "/api/v1/dpc")
		})

	// ---- children ---------------------------------------------------------------------------

	add(server, client, "create_child",
		"Add a child to the family. Then add_device and get_setup_code to set up their phone.",
		func(ctx context.Context, c *fgclient.Client, in createChildArgs) (any, error) {
			return doJSON(ctx, c, "POST", "/api/v1/children", in)
		})

	add(server, client, "update_child",
		"Rename a child or set their birth year.",
		func(ctx context.Context, c *fgclient.Client, in updateChildArgs) (any, error) {
			return doJSON(ctx, c, "PATCH", "/api/v1/children/"+in.ChildID,
				map[string]any{"name": in.Name, "birth_year": in.BirthYear})
		})

	add(server, client, "set_policy",
		"Change a child's rules: the Tageszeit (daily limit), bedtime, timezone, YouTube, installs, uninstalls, "+
			"debugging, watch-only, the ad filter and Private DNS. Send only what changes; the phones apply it "+
			"within seconds. Returns the whole policy. Use adjust_time_today for Extrazeit (today only).",
		func(ctx context.Context, c *fgclient.Client, in policyArgs) (any, error) {
			body := map[string]any{}
			for key, value := range map[string]any{
				"daily_limit_minutes": in.DailyLimitMinutes, "bedtime_enabled": in.BedtimeEnabled,
				"bedtime_start": in.BedtimeStart, "bedtime_end": in.BedtimeEnd, "timezone": in.Timezone,
				"youtube_blocked": in.YouTubeBlocked, "allow_child_installs": in.AllowChildInstalls,
				"allow_uninstall": in.AllowUninstall, "allow_debugging": in.AllowDebugging,
				"tracking_only": in.TrackingOnly, "ad_filter": in.AdFilter,
				"ad_filter_list_url": in.AdFilterListURL, "dns_host": in.DNSHost,
			} {
				if !isNilPointer(value) {
					body[key] = value
				}
			}
			if len(body) == 0 {
				return nil, fmt.Errorf("nothing to change: name at least one setting")
			}
			return doJSON(ctx, c, "PATCH", "/api/v1/children/"+in.ChildID+"/policy", body)
		})

	add(server, client, "answer_time_request",
		"Answer a child's \"Mehr Zeit erbitten\" (FR-28.5), from get_today's time_requests. grant gives "+
			"Extrazeit for today only — it ends at midnight — the asked minutes or the ones you name; "+
			"decline says no. The phone hears of it at once and tells the child.",
		func(ctx context.Context, c *fgclient.Client, in answerArgs) (any, error) {
			decision := strings.ToLower(strings.TrimSpace(in.Decision))
			if decision != "grant" && decision != "decline" {
				return nil, fmt.Errorf("decision must be grant or decline, not %q", in.Decision)
			}
			body := map[string]any{"decision": decision}
			if decision == "grant" && in.Minutes != 0 {
				body["minutes"] = in.Minutes
			}
			return doJSON(ctx, c, "POST",
				"/api/v1/children/"+in.ChildID+"/time-requests/"+in.RequestID+"/decision", body)
		})

	// ---- apps and domains -------------------------------------------------------------------

	add(server, client, "list_app_rules",
		"A child's app rules: ALLOW, LIMIT (with its own minutes), BONUS or BLOCK per package. An app "+
			"with no rule counts against the daily time, or waits for a decision when installs need one.",
		func(ctx context.Context, c *fgclient.Client, in childArgs) (any, error) {
			return getJSON(ctx, c, "/api/v1/children/"+in.ChildID+"/app-rules")
		})

	add(server, client, "set_app_rule",
		"Decide how one app may be used on a child's phones: ALLOW, LIMIT (optionally with its own daily "+
			"minutes), BONUS (only on Bonuszeit), BLOCK, or NONE to remove the rule.",
		func(ctx context.Context, c *fgclient.Client, in appRuleArgs) (any, error) {
			rule := strings.ToUpper(strings.TrimSpace(in.Rule))
			path := "/api/v1/children/" + in.ChildID + "/app-rules"
			switch rule {
			case "NONE":
				if err := c.Do(ctx, "DELETE", fgclient.Query(path, map[string]string{"package_name": in.PackageName}), nil, nil); err != nil {
					return nil, err
				}
				return map[string]any{"package_name": in.PackageName, "rule": "NONE"}, nil
			case store.ActionAllow, store.ActionLimit, store.ActionBonus, store.ActionBlock:
				limit := 0
				if rule == store.ActionLimit {
					limit = in.LimitMinutes
				}
				return doJSON(ctx, c, "PUT", path,
					map[string]any{"package_name": in.PackageName, "action": rule, "limit_minutes": limit})
			default:
				return nil, fmt.Errorf("rule must be ALLOW, LIMIT, BONUS, BLOCK or NONE, not %q", in.Rule)
			}
		})

	add(server, client, "list_blocked_domains",
		"The web domains blocked on a child's phones.",
		func(ctx context.Context, c *fgclient.Client, in childArgs) (any, error) {
			return getJSON(ctx, c, "/api/v1/children/"+in.ChildID+"/blocked-domains")
		})

	add(server, client, "set_blocked_domain",
		"Block a web domain (and its subdomains) on a child's phones, or lift the block.",
		func(ctx context.Context, c *fgclient.Client, in domainArgs) (any, error) {
			path := "/api/v1/children/" + in.ChildID + "/blocked-domains"
			if !in.Blocked {
				if err := c.Do(ctx, "DELETE", fgclient.Query(path, map[string]string{"domain": in.Domain}), nil, nil); err != nil {
					return nil, err
				}
				return map[string]any{"domain": in.Domain, "blocked": false}, nil
			}
			return doJSON(ctx, c, "POST", path, map[string]string{"domain": in.Domain})
		})

	add(server, client, "set_family_blocked_package",
		"Put an app on the family's blocklist — no child may have it, and it is removed from their phones — "+
			"or take it off. list_blocked_packages shows the list.",
		func(ctx context.Context, c *fgclient.Client, in familyBlockArgs) (any, error) {
			path := "/api/v1/family/blocked-packages"
			if !in.Blocked {
				if err := c.Do(ctx, "DELETE", fgclient.Query(path, map[string]string{"package_name": in.PackageName}), nil, nil); err != nil {
					return nil, err
				}
				return map[string]any{"package_name": in.PackageName, "blocked": false}, nil
			}
			return doJSON(ctx, c, "PUT", path, map[string]string{
				"package_name": in.PackageName, "label": in.Label, "reason": in.Reason})
		})

	// ---- phones -----------------------------------------------------------------------------

	add(server, client, "add_device",
		"Add a phone to a child. It is not set up yet: get_setup_code gives the QR the phone scans after a "+
			"factory reset.",
		func(ctx context.Context, c *fgclient.Client, in addDeviceArgs) (any, error) {
			return doJSON(ctx, c, "POST", "/api/v1/children/"+in.ChildID+"/devices", map[string]string{"name": in.Name})
		})

	add(server, client, "rename_device",
		"Rename a phone.",
		func(ctx context.Context, c *fgclient.Client, in renameDeviceArgs) (any, error) {
			return doJSON(ctx, c, "PATCH", "/api/v1/devices/"+in.DeviceID, map[string]string{"name": in.Name})
		})

	add(server, client, "get_setup_code",
		"Make a NEW setup code for a phone: the QR (as SVG) a freshly reset phone scans on its welcome screen "+
			"after six taps, and the same single-use token spelled out for 'Dieses Handy neu verbinden' on a "+
			"phone that is already set up. A new code revokes the previous one, and for a phone that is set "+
			"up, replace_enrolled must be true — it then replaces that phone's credential.",
		func(ctx context.Context, c *fgclient.Client, in setupCodeArgs) (any, error) {
			return doJSON(ctx, c, "POST", "/api/v1/devices/"+in.DeviceID+"/provisioning",
				map[string]bool{"replace_enrolled": in.ReplaceEnrolled})
		})

	add(server, client, "get_recovery_code",
		"A phone's recovery code: typed on the phone under 'Für Eltern' it releases the phone from the rules "+
			"until it next reaches the server — for a phone that is offline and locked. Treat it as a secret.",
		func(ctx context.Context, c *fgclient.Client, in deviceArgs) (any, error) {
			return getJSON(ctx, c, "/api/v1/devices/"+in.DeviceID+"/recovery-code")
		})

	add(server, client, "list_recovery_events",
		"When a phone was released with its recovery code, and failed attempts.",
		func(ctx context.Context, c *fgclient.Client, in deviceArgs) (any, error) {
			return getJSON(ctx, c, "/api/v1/devices/"+in.DeviceID+"/recovery-events")
		})

	add(server, client, "get_desired_state",
		"What a phone does right now and why: which apps are paused (and the reason: PAUSED, QUOTA, "+
			"BEDTIME, EARNED), the Tageszeit, Extrazeit and minutes used, Bonuszeit left, apps waiting for a "+
			"decision, and when the next change happens.",
		func(ctx context.Context, c *fgclient.Client, in deviceArgs) (any, error) {
			return getJSON(ctx, c, "/api/v1/devices/"+in.DeviceID+"/desired-state")
		})

	add(server, client, "list_device_apps",
		"The apps installed on a phone as it last reported them, with their labels.",
		func(ctx context.Context, c *fgclient.Client, in deviceAppsArgs) (any, error) {
			params := map[string]string{}
			if in.IncludeSystem {
				params["include_system"] = "1"
			}
			return getJSON(ctx, c, fgclient.Query("/api/v1/devices/"+in.DeviceID+"/apps", params))
		})

	add(server, client, "forget_device_app",
		"Take an app the phone no longer has off its list (it reappears if the phone reports it again).",
		func(ctx context.Context, c *fgclient.Client, in devicePackageArgs) (any, error) {
			if err := c.Do(ctx, "DELETE", "/api/v1/devices/"+in.DeviceID+"/apps/"+url.PathEscape(in.PackageName), nil, nil); err != nil {
				return nil, err
			}
			return map[string]any{"package_name": in.PackageName, "forgotten": true}, nil
		})

	add(server, client, "get_timeline",
		"One day of a phone, as Aktivität shows it: screen time per hour, each app's minutes and rule, "+
			"the minutes counted against the limit and those paid from Bonuszeit, and every sitting.",
		func(ctx context.Context, c *fgclient.Client, in timelineArgs) (any, error) {
			return getJSON(ctx, c, fgclient.Query("/api/v1/devices/"+in.DeviceID+"/usage/timeline",
				map[string]string{"day": in.Day}))
		})

	// ---- the app catalog and managed apps -----------------------------------------------------

	add(server, client, "upload_app",
		"Add an APK from this computer to the catalog, so it can be installed on children's phones with "+
			"set_managed_app.",
		func(ctx context.Context, c *fgclient.Client, in uploadArgs) (any, error) {
			if !filepath.IsAbs(in.Path) {
				return nil, fmt.Errorf("path must be absolute, not %q", in.Path)
			}
			f, err := os.Open(in.Path)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			var out map[string]any
			if err := c.Upload(ctx, fgclient.Query("/api/v1/apps", map[string]string{"label": in.Label}),
				f, "application/vnd.android.package-archive", &out); err != nil {
				return nil, err
			}
			return out, nil
		})

	add(server, client, "scan_apps",
		"Register the APKs placed in the server's app directory by hand.",
		func(ctx context.Context, c *fgclient.Client, _ emptyArgs) (any, error) {
			return doJSON(ctx, c, "POST", "/api/v1/apps/scan", nil)
		})

	add(server, client, "delete_app",
		"Remove an APK version from the catalog. Phones that have it keep it.",
		func(ctx context.Context, c *fgclient.Client, in appIDArgs) (any, error) {
			if err := c.Do(ctx, "DELETE", "/api/v1/apps/"+in.AppID, nil, nil); err != nil {
				return nil, err
			}
			return map[string]any{"app_id": in.AppID, "deleted": true}, nil
		})

	add(server, client, "list_managed_apps",
		"The catalog apps a child's phones install and keep current.",
		func(ctx context.Context, c *fgclient.Client, in childArgs) (any, error) {
			return getJSON(ctx, c, "/api/v1/children/"+in.ChildID+"/managed-apps")
		})

	add(server, client, "set_managed_app",
		"Install a catalog app on a child's phones and keep it current, or remove it from them.",
		func(ctx context.Context, c *fgclient.Client, in managedArgs) (any, error) {
			method := "PUT"
			if !in.Managed {
				method = "DELETE"
			}
			if err := c.Do(ctx, method, "/api/v1/children/"+in.ChildID+"/managed-apps/"+url.PathEscape(in.PackageName), nil, nil); err != nil {
				return nil, err
			}
			return map[string]any{"package_name": in.PackageName, "managed": in.Managed}, nil
		})
}

// getJSON and doJSON decode the answer before returning it. Not `return out, c.Get(…, &out)`: Go
// leaves unspecified whether `out` is read before or after the call fills it.
func getJSON(ctx context.Context, c *fgclient.Client, path string) (any, error) {
	var out any
	if err := c.Get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func doJSON(ctx context.Context, c *fgclient.Client, method, path string, body any) (any, error) {
	var out any
	if err := c.Do(ctx, method, path, body, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// isNilPointer reports whether v is a typed nil pointer — an argument the caller did not send.
func isNilPointer(v any) bool {
	switch p := v.(type) {
	case *int:
		return p == nil
	case *bool:
		return p == nil
	case *string:
		return p == nil
	}
	return v == nil
}
