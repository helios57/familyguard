package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"

	"github.com/helios57/familyguard/backend/internal/fgclient"
	"github.com/helios57/familyguard/backend/internal/store"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// cmdMCP serves the same operations as the CLI over MCP on stdio.
//
// It shares the client and the response types with the CLI rather than re-implementing them, so
// there is exactly one place where a field name can be wrong. The tools are task-shaped rather than
// one-per-route: the API has around forty-five parent endpoints and a forty-five-tool server is one
// a model cannot choose within.
//
// Deliberately absent: the four deletes. An API key is the parent that created it, so the
// capability exists regardless -- withholding the tool only means a model must go through the CLI,
// where a human types --yes.
func cmdMCP(ctx context.Context, env *environment, _ []string) error {
	// Stdout is the transport. Anything written there that is not JSON-RPC corrupts the session, so
	// nothing in this path may print -- which is why errors travel back as tool results.
	return runMCP(ctx, env.client, os.Stdin, env.out)
}

// runMCP is cmdMCP with the streams passed in, so the shutdown behaviour below can be tested
// without a subprocess.
//
// It builds the transport out of mcp.IOTransport rather than mcp.StdioTransport. Those are the
// same thing -- StdioTransport.Connect is literally IOTransport over os.Stdin and os.Stdout -- but
// supplying the reader is what lets this function observe EOF.
func runMCP(ctx context.Context, client *fgclient.Client, in io.Reader, out io.Writer) error {
	server := mcp.NewServer(&mcp.Implementation{
		Name:    "familyguard",
		Version: version,
	}, nil)
	registerTools(server, client)

	watched := &eofWatcher{r: in}
	err := server.Run(ctx, &mcp.IOTransport{
		Reader: io.NopCloser(watched),
		// The SDK's own StdioTransport wraps stdout in a no-op closer. Handing it a *os.File
		// directly would let it close the process's stdout on shutdown.
		Writer: nopWriteCloser{out},
	})
	if err != nil && watched.sawEOF.Load() {
		// The client closed the pipe, which is how an MCP client stops a stdio server. The SDK
		// reports it as an error once a session has been established -- Server.Run returns
		// "server is closing: EOF" -- so returning it unchanged makes every ordinary shutdown
		// exit 1 and read as a crash to whatever supervises the process.
		return nil
	}
	return err
}

// eofWatcher records whether the stream ended, so a clean client disconnect can be told apart from
// a transport failure.
//
// The alternative is to match the SDK's message, because the sentinel it wraps
// (jsonrpc2.ErrServerClosing) lives under the SDK's internal/ and cannot be imported. That would be
// a string comparison against another module's private wording, which changes without notice and
// fails silently in the direction that hides errors. Watching the reader answers the question at
// the one place that can know it for certain.
type eofWatcher struct {
	r      io.Reader
	sawEOF atomic.Bool
}

func (w *eofWatcher) Read(p []byte) (int, error) {
	n, err := w.r.Read(p)
	if errors.Is(err, io.EOF) {
		// Atomic because the SDK reads on its own goroutine; Run returning does not by itself
		// establish a happens-before edge the race detector will accept.
		w.sawEOF.Store(true)
	}
	return n, err
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

// ---- tool argument types --------------------------------------------------
//
// Field comments become the JSON-schema descriptions the model reads, via the jsonschema tag, so
// they are written for that audience and not for a maintainer.

type emptyArgs struct{}

type childArgs struct {
	ChildID string `json:"child_id" jsonschema:"the child's UUID, as returned by list_children"`
}

type deviceArgs struct {
	DeviceID string `json:"device_id" jsonschema:"the device's UUID, as returned by list_devices"`
}

type energyArgs struct {
	DeviceID string `json:"device_id" jsonschema:"the device's UUID, as returned by list_devices"`
	Hours    int    `json:"hours,omitempty" jsonschema:"how many hours back, 1 to 744; default 24"`
}

type commandsArgs struct {
	DeviceID string `json:"device_id" jsonschema:"the device's UUID"`
	Limit    int    `json:"limit,omitempty" jsonschema:"how many to return, 1-200, default 50"`
}

type sendArgs struct {
	DeviceID string `json:"device_id" jsonschema:"the device's UUID"`
	Type     string `json:"type" jsonschema:"one of TRIGGER_ALARM, STOP_ALARM, LOCK_NOW, UNLOCK_DEVICE, LOCATE_NOW, BLOCK_YOUTUBE_ALL, UNBLOCK_YOUTUBE_ALL, SYNC_POLICY, UPDATE_APP"`
}

type pauseArgs struct {
	ChildID string `json:"child_id" jsonschema:"the child's UUID, as returned by list_children"`
	Paused  bool   `json:"paused" jsonschema:"true pauses every app the child can open except calls and messages; false lifts it"`
}

type adjustArgs struct {
	ChildID string `json:"child_id" jsonschema:"the child's UUID, as returned by list_children"`
	Minutes int    `json:"minutes" jsonschema:"minutes to add (positive) or take away (negative) for today only, -1440 to 1440, not 0"`
}

// The plan's own argument types, rather than store.PlanGroup: a uuid.UUID is a [16]byte, and the
// schema the SDK derives from it tells a model that an id is an ARRAY, so no model could ever send a
// valid set_plan. The ids travel as the strings get_plan shows, and an absent one is omitted, which
// is what the server reads as "new".
type planTaskArg struct {
	ID    string `json:"id,omitempty" jsonschema:"the task's id from get_plan; omit for a new task"`
	Title string `json:"title" jsonschema:"what the child does, at most 120 characters"`
	Note  string `json:"note,omitempty" jsonschema:"optional detail such as '10 min', at most 200 characters"`
}

type planGroupArg struct {
	ID            string        `json:"id,omitempty" jsonschema:"the group's id from get_plan; omit for a new group"`
	Title         string        `json:"title" jsonschema:"the group's name, such as Morgen"`
	Weekdays      int           `json:"weekdays" jsonschema:"bit set: Monday 1, Tuesday 2, … Sunday 64; 127 is every day, 31 Monday to Friday"`
	StartsAt      string        `json:"starts_at" jsonschema:"HH:MM, when the child can start reporting these tasks"`
	EndsAt        string        `json:"ends_at" jsonschema:"HH:MM, after starts_at on the same day"`
	EarnedMinutes int           `json:"earned_minutes" jsonschema:"minutes of earned time the group earns once every task is confirmed, 0-1440"`
	Tasks         []planTaskArg `json:"tasks" jsonschema:"1 to 20 tasks"`
}

type planArgs struct {
	ChildID string         `json:"child_id" jsonschema:"the child's UUID, as returned by list_children"`
	Groups  []planGroupArg `json:"groups" jsonschema:"the WHOLE plan: every group to keep, each with its id as get_plan returned it; a group or task without an id is new, one left out is retired"`
}

type decideArgs struct {
	ChildID  string `json:"child_id" jsonschema:"the child's UUID, as returned by list_children"`
	TaskID   string `json:"task_id" jsonschema:"the task's UUID, as returned by get_today"`
	Decision string `json:"decision" jsonschema:"confirm, reject or undo"`
}

type alarmArgs struct {
	ChildID      string   `json:"child_id" jsonschema:"the child's UUID, as returned by list_children"`
	Weekdays     []string `json:"weekdays" jsonschema:"seven entries, Monday first, each HH:MM or an empty string for a day with no alarm"`
	SkipHolidays *bool    `json:"skip_holidays,omitempty" jsonschema:"true: no alarm on the family's holidays (a date changed on its own still rings); omit to leave it as it is"`
}

// String ids for the same reason as planGroupArg: a uuid.UUID's schema reads as an array.
type agendaEntryArg struct {
	ID       string `json:"id,omitempty" jsonschema:"the entry's id from get_agenda; omit for a new entry"`
	Kind     string `json:"kind" jsonschema:"RECURRING (on weekdays) or SINGLE (on one date)"`
	Title    string `json:"title" jsonschema:"what it is, such as Schule"`
	Place    string `json:"place,omitempty" jsonschema:"where, optional"`
	Optional bool   `json:"optional,omitempty" jsonschema:"true when the child may skip it"`
	Weekdays int    `json:"weekdays,omitempty" jsonschema:"RECURRING only: bit set Monday 1 … Sunday 64; 31 is Monday to Friday"`
	Day      string `json:"day,omitempty" jsonschema:"SINGLE only: YYYY-MM-DD"`
	StartsAt string `json:"starts_at" jsonschema:"HH:MM"`
	EndsAt   string `json:"ends_at" jsonschema:"HH:MM, after starts_at on the same day"`
}

type agendaArgs struct {
	ChildID string           `json:"child_id" jsonschema:"the child's UUID, as returned by list_children"`
	Entries []agendaEntryArg `json:"entries" jsonschema:"the WHOLE agenda: every entry to keep, with its id from get_agenda; one left out is retired"`
}

type weekArgs struct {
	ChildID string `json:"child_id" jsonschema:"the child's UUID, as returned by list_children"`
	From    string `json:"from,omitempty" jsonschema:"YYYY-MM-DD, default today in the child's timezone"`
	Days    int    `json:"days,omitempty" jsonschema:"1 to 31, default 7"`
}

type holidayArg struct {
	ID       string `json:"id,omitempty" jsonschema:"the holiday's id from get_holidays; omit for a new one"`
	Title    string `json:"title" jsonschema:"such as Herbstferien"`
	StartsOn string `json:"starts_on" jsonschema:"first day, YYYY-MM-DD"`
	EndsOn   string `json:"ends_on" jsonschema:"last day, YYYY-MM-DD, at most 120 days after the first"`
}

type calendarArgs struct {
	ChildID string `json:"child_id" jsonschema:"the child's UUID, as returned by list_children"`
	URL     string `json:"url" jsonschema:"an https:// or webcal:// iCalendar (.ics) address, read at once; an empty string removes the calendar"`
}

type holidaysArgs struct {
	Holidays []holidayArg `json:"holidays" jsonschema:"ALL the family's holidays: every one to keep, with its id; one left out is retired"`
}

type alarmDayArgs struct {
	ChildID string `json:"child_id" jsonschema:"the child's UUID, as returned by list_children"`
	Day     string `json:"day" jsonschema:"YYYY-MM-DD in the child's timezone, today up to 60 days ahead, or today / tomorrow"`
	Time    string `json:"time" jsonschema:"HH:MM to ring at, off for no alarm that day, or clear to return the day to the week"`
}

type auditArgs struct {
	Limit int `json:"limit,omitempty" jsonschema:"how many entries, 1-500, default 100"`
}

// registerTools wires every tool. Kept as one function so the tool list is readable as a list.
func registerTools(server *mcp.Server, client *fgclient.Client) {
	add(server, client, "list_children",
		"List the children in this family, with their IDs.",
		func(ctx context.Context, c *fgclient.Client, _ emptyArgs) (any, error) {
			var body struct {
				Children []store.Child `json:"children"`
			}
			if err := c.Get(ctx, "/api/v1/children", &body); err != nil {
				return nil, err
			}
			return body.Children, nil
		})

	add(server, client, "list_devices",
		"List every enrolled device with its model, OS version and lock state.",
		func(ctx context.Context, c *fgclient.Client, _ emptyArgs) (any, error) {
			var body struct {
				Devices []store.Device `json:"devices"`
			}
			if err := c.Get(ctx, "/api/v1/devices", &body); err != nil {
				return nil, err
			}
			return body.Devices, nil
		})

	add(server, client, "get_device",
		"One device with everything the phone has reported: battery, connectivity, DPC build, "+
			"and whether Android is letting it keep its own schedule. A field that is null means "+
			"the phone has not reported it, which is not the same as false.",
		func(ctx context.Context, c *fgclient.Client, in deviceArgs) (any, error) {
			view, err := fetchDevice(ctx, c, in.DeviceID)
			if err != nil {
				return nil, err
			}
			// The advisory is computed here rather than left to the model, because the null/false
			// distinction is the whole point and a model reading raw JSON will flatten it.
			result := map[string]any{"device": view.Device, "state": view.State, "enrolled": view.Enrolled}
			if view.State != nil {
				result["background_restriction"] = restrictionAdvice(view.State)
			}
			return result, nil
		})

	add(server, client, "get_policy",
		"The policy in force for a child: daily limit, bedtime, YouTube, install and debugging rules.",
		func(ctx context.Context, c *fgclient.Client, in childArgs) (any, error) {
			var pol store.Policy
			if err := c.Get(ctx, "/api/v1/children/"+in.ChildID+"/policy", &pol); err != nil {
				return nil, err
			}
			return pol, nil
		})

	add(server, client, "pause_profile",
		"Pause or unpause a child's phones (FR-21). Paused, every app the child can open is suspended "+
			"except the dialer, SMS and the family's messengers (WhatsApp, Signal, Threema); it lasts "+
			"until unpaused, through reboots and with the phone offline.",
		func(ctx context.Context, c *fgclient.Client, in pauseArgs) (any, error) {
			var out map[string]any
			if err := c.Do(ctx, "POST", "/api/v1/children/"+in.ChildID+"/pause",
				map[string]bool{"paused": in.Paused}, &out); err != nil {
				return nil, err
			}
			return out, nil
		})

	add(server, client, "adjust_time_today",
		"Add or take away screen time for a child for today only (FR-3.11, FR-21): +30 gives half an "+
			"hour more, -15 takes a quarter of an hour away. The day's limit never goes below zero; "+
			"the child must have a daily limit. Returns the day's total adjustment.",
		func(ctx context.Context, c *fgclient.Client, in adjustArgs) (any, error) {
			if in.Minutes == 0 || in.Minutes < -1440 || in.Minutes > 1440 {
				return nil, fmt.Errorf("minutes must be from -1440 to 1440 and not 0, not %d", in.Minutes)
			}
			var out map[string]any
			if err := c.Do(ctx, "POST", "/api/v1/children/"+in.ChildID+"/bonus",
				map[string]int{"minutes": in.Minutes}, &out); err != nil {
				return nil, err
			}
			return out, nil
		})

	add(server, client, "get_plan",
		"A child's daily plan (FR-22): groups of tasks, each with its weekdays (a bit set, Monday = 1 "+
			"… Sunday = 64, 127 = every day), its window, and the minutes of earned time (Bonuszeit) it "+
			"earns once every task in it is confirmed.",
		func(ctx context.Context, c *fgclient.Client, in childArgs) (any, error) {
			var plan planDoc
			if err := c.Get(ctx, "/api/v1/children/"+in.ChildID+"/plan", &plan); err != nil {
				return nil, err
			}
			return plan, nil
		})

	add(server, client, "set_plan",
		"Replace a child's daily plan with the given groups. Send the whole plan: call get_plan first "+
			"and keep the ids, because a group that is left out is retired and one sent without its id "+
			"starts a new history.",
		func(ctx context.Context, c *fgclient.Client, in planArgs) (any, error) {
			var plan planDoc
			if err := c.Do(ctx, "PUT", "/api/v1/children/"+in.ChildID+"/plan", map[string]any{"groups": in.Groups}, &plan); err != nil {
				return nil, err
			}
			return plan, nil
		})

	add(server, client, "get_today",
		"Today's tasks for a child with their states (OPEN, REPORTED by the child, CONFIRMED, REJECTED), "+
			"and the earned time: available, spent today, left (negative is a debt), and when each "+
			"credit expires.",
		func(ctx context.Context, c *fgclient.Client, in childArgs) (any, error) {
			var day todayDoc
			if err := c.Get(ctx, "/api/v1/children/"+in.ChildID+"/today", &day); err != nil {
				return nil, err
			}
			return day, nil
		})

	add(server, client, "decide_task",
		"Confirm, reject or undo a task for today. Confirming a group's last open task earns the group's "+
			"minutes; a task need not be reported first. Undo or reject on a complete group takes its "+
			"minutes back.",
		func(ctx context.Context, c *fgclient.Client, in decideArgs) (any, error) {
			decision := strings.ToLower(strings.TrimSpace(in.Decision))
			if decision != "confirm" && decision != "reject" && decision != "undo" {
				return nil, fmt.Errorf("decision must be confirm, reject or undo, not %q", in.Decision)
			}
			var out map[string]any
			if err := c.Do(ctx, "POST", "/api/v1/children/"+in.ChildID+"/tasks/"+in.TaskID+"/decision",
				map[string]string{"decision": decision}, &out); err != nil {
				return nil, err
			}
			return out, nil
		})

	add(server, client, "get_alarm",
		"A child's alarm clock (FR-23): seven weekdays, Monday first, each HH:MM or empty for no alarm, "+
			"and the date changes from today on (time null = no alarm that day).",
		func(ctx context.Context, c *fgclient.Client, in childArgs) (any, error) {
			var alarm store.Alarm
			if err := c.Get(ctx, "/api/v1/children/"+in.ChildID+"/alarm", &alarm); err != nil {
				return nil, err
			}
			return alarm, nil
		})

	add(server, client, "set_alarm",
		"Replace a child's alarm week. The phone rings at these times with no connection needed; the "+
			"child can stop or snooze it but not change it.",
		func(ctx context.Context, c *fgclient.Client, in alarmArgs) (any, error) {
			var alarm store.Alarm
			body := map[string]any{"weekdays": in.Weekdays}
			if in.SkipHolidays != nil {
				body["skip_holidays"] = *in.SkipHolidays
			}
			if err := c.Do(ctx, "PUT", "/api/v1/children/"+in.ChildID+"/alarm", body, &alarm); err != nil {
				return nil, err
			}
			return alarm, nil
		})

	add(server, client, "set_alarm_day",
		"Change the alarm for one date: a time, off for no alarm, or clear to follow the week again.",
		func(ctx context.Context, c *fgclient.Client, in alarmDayArgs) (any, error) {
			day, err := resolveProfileDay(ctx, c, in.ChildID, in.Day)
			if err != nil {
				return nil, err
			}
			path := "/api/v1/children/" + in.ChildID + "/alarm/days/" + day
			switch {
			case in.Time == "clear":
				if err := c.Do(ctx, "DELETE", path, nil, nil); err != nil {
					return nil, err
				}
				return map[string]any{"day": day, "cleared": true}, nil
			case in.Time == "off" || clockText.MatchString(in.Time):
				body := map[string]any{"time": nil}
				if in.Time != "off" {
					body["time"] = in.Time
				}
				var out store.AlarmDay
				if err := c.Do(ctx, "PUT", path, body, &out); err != nil {
					return nil, err
				}
				return out, nil
			default:
				return nil, fmt.Errorf("time is HH:MM, off or clear, not %q", in.Time)
			}
		})

	add(server, client, "get_agenda",
		"A child's agenda (FR-24): entries repeating on weekdays (school, training) or on one date, "+
			"each with a time, an optional place and whether it is optional. Use get_week to see it laid out.",
		func(ctx context.Context, c *fgclient.Client, in childArgs) (any, error) {
			var out map[string]any
			if err := c.Get(ctx, "/api/v1/children/"+in.ChildID+"/agenda", &out); err != nil {
				return nil, err
			}
			return out, nil
		})

	add(server, client, "set_agenda",
		"Replace a child's agenda. Send the whole agenda: call get_agenda first and keep the ids.",
		func(ctx context.Context, c *fgclient.Client, in agendaArgs) (any, error) {
			var out map[string]any
			if err := c.Do(ctx, "PUT", "/api/v1/children/"+in.ChildID+"/agenda", map[string]any{"entries": in.Entries}, &out); err != nil {
				return nil, err
			}
			return out, nil
		})

	add(server, client, "get_week",
		"A child's agenda laid out over days, in time order, with the family holiday that applies — "+
			"holidays pause repeating entries, not single ones. This is what the phone shows.",
		func(ctx context.Context, c *fgclient.Client, in weekArgs) (any, error) {
			days := ""
			if in.Days > 0 {
				days = fmt.Sprint(in.Days)
			}
			return fetchWeek(ctx, c, in.ChildID, in.From, days)
		})

	add(server, client, "get_holidays",
		"The family's holidays: first and last day of each, both included.",
		func(ctx context.Context, c *fgclient.Client, _ emptyArgs) (any, error) {
			var out map[string]any
			if err := c.Get(ctx, "/api/v1/family/holidays", &out); err != nil {
				return nil, err
			}
			return out, nil
		})

	add(server, client, "set_holidays",
		"Replace the family's holidays. Send all of them: call get_holidays first and keep the ids.",
		func(ctx context.Context, c *fgclient.Client, in holidaysArgs) (any, error) {
			var out map[string]any
			if err := c.Do(ctx, "PUT", "/api/v1/family/holidays", map[string]any{"holidays": in.Holidays}, &out); err != nil {
				return nil, err
			}
			return out, nil
		})

	add(server, client, "get_calendar",
		"A child's calendar read into the agenda (FR-25): its address, when it was last read, how many events "+
			"it holds in the coming 60 days, and the error of the last read if it failed.",
		func(ctx context.Context, c *fgclient.Client, in childArgs) (any, error) {
			var out calendarState
			if err := c.Get(ctx, "/api/v1/children/"+in.ChildID+"/calendar", &out); err != nil {
				return nil, err
			}
			return out, nil
		})

	add(server, client, "set_calendar",
		"Set a child's calendar address, which is read at once and refused if it is not an iCalendar file; "+
			"its events then appear in get_week and on the phone. An empty url removes it.",
		func(ctx context.Context, c *fgclient.Client, in calendarArgs) (any, error) {
			path := "/api/v1/children/" + in.ChildID + "/calendar"
			if strings.TrimSpace(in.URL) == "" {
				if err := c.Do(ctx, "DELETE", path, nil, nil); err != nil {
					return nil, err
				}
				return map[string]any{"removed": true}, nil
			}
			var out calendarState
			if err := c.Do(ctx, "PUT", path, map[string]string{"url": in.URL}, &out); err != nil {
				return nil, err
			}
			return out, nil
		})

	add(server, client, "list_commands",
		"The command queue for a device, newest first, with created/delivered/acked timestamps. "+
			"A large gap between created and delivered means the phone was asleep or restricted, "+
			"not that the command failed.",
		func(ctx context.Context, c *fgclient.Client, in commandsArgs) (any, error) {
			var body struct {
				Commands []store.Command `json:"commands"`
			}
			params := map[string]string{}
			if in.Limit > 0 {
				params["limit"] = fmt.Sprint(in.Limit)
			}
			path := fgclient.Query("/api/v1/devices/"+in.DeviceID+"/commands", params)
			if err := c.Get(ctx, path, &body); err != nil {
				return nil, err
			}
			return body.Commands, nil
		})

	add(server, client, "send_command",
		"Queue a command for a device. It is QUEUED, not delivered: the phone collects it on its "+
			"next connection, which on a battery-restricted phone can be minutes. Use list_commands "+
			"to see when it was actually delivered and acknowledged.",
		func(ctx context.Context, c *fgclient.Client, in sendArgs) (any, error) {
			commandType := strings.ToUpper(strings.TrimSpace(in.Type))
			if !store.ValidCommandTypes[commandType] {
				return nil, fmt.Errorf("%q is not a command this server accepts (one of: %s)",
					in.Type, strings.Join(sortedKeys(store.ValidCommandTypes), ", "))
			}
			var created store.Command
			body := map[string]any{"type": commandType}
			if err := c.Do(ctx, "POST", "/api/v1/devices/"+in.DeviceID+"/commands", body, &created); err != nil {
				return nil, err
			}
			return created, nil
		})

	add(server, client, "list_apps",
		"The APKs this deployment hosts for installation on children's phones.",
		func(ctx context.Context, c *fgclient.Client, _ emptyArgs) (any, error) {
			var body struct {
				Apps       []map[string]any `json:"apps"`
				Configured bool             `json:"configured"`
			}
			if err := c.Get(ctx, "/api/v1/apps", &body); err != nil {
				return nil, err
			}
			return body, nil
		})

	add(server, client, "list_blocked_packages",
		"The family-wide blocked package list.",
		func(ctx context.Context, c *fgclient.Client, _ emptyArgs) (any, error) {
			var body struct {
				Packages []map[string]any `json:"packages"`
			}
			if err := c.Get(ctx, "/api/v1/family/blocked-packages", &body); err != nil {
				return nil, err
			}
			return body.Packages, nil
		})

	add(server, client, "get_usage",
		"Screen time a device has reported. Empty means nothing was reported, which is a "+
			"measurement that was not taken rather than a child who used nothing.",
		func(ctx context.Context, c *fgclient.Client, in deviceArgs) (any, error) {
			var body map[string]any
			if err := c.Get(ctx, "/api/v1/devices/"+in.DeviceID+"/usage", &body); err != nil {
				return nil, err
			}
			return body, nil
		})

	add(server, client, "get_locations",
		"Locations a device has reported, newest first.",
		func(ctx context.Context, c *fgclient.Client, in deviceArgs) (any, error) {
			var body struct {
				Locations []map[string]any `json:"locations"`
			}
			if err := c.Get(ctx, "/api/v1/devices/"+in.DeviceID+"/locations", &body); err != nil {
				return nil, err
			}
			return body.Locations, nil
		})

	add(server, client, "get_energy",
		"What FamilyGuard spends on a phone per hour as the phone measured it (FR-26.5): its CPU time, "+
			"the wake-ups it caused by kind, its data, and the battery drop over unplugged intervals.",
		func(ctx context.Context, c *fgclient.Client, in energyArgs) (any, error) {
			hours := ""
			if in.Hours > 0 {
				hours = fmt.Sprint(in.Hours)
			}
			body, err := getEnergy(ctx, c, in.DeviceID, hours)
			if err != nil {
				return nil, err
			}
			return map[string]any{"hours": body.Hours, "total": body.Total, "samples": body.Samples,
				"summary": energySummary(body.Total)}, nil
		})

	add(server, client, "list_audit",
		"The audit log, newest first: who did what, including whether an API key or a browser did it.",
		func(ctx context.Context, c *fgclient.Client, in auditArgs) (any, error) {
			var body struct {
				Entries []store.AuditEntry `json:"entries"`
			}
			params := map[string]string{}
			if in.Limit > 0 {
				params["limit"] = fmt.Sprint(in.Limit)
			}
			if err := c.Get(ctx, fgclient.Query("/api/v1/audit", params), &body); err != nil {
				return nil, err
			}
			return body.Entries, nil
		})

	add(server, client, "whoami",
		"Which parent this credential acts as, and with what role.",
		func(ctx context.Context, c *fgclient.Client, _ emptyArgs) (any, error) {
			var parent store.Parent
			if err := c.Get(ctx, "/api/v1/me", &parent); err != nil {
				return nil, err
			}
			return parent, nil
		})
}

// restrictionAdvice turns the two three-valued booleans into something a model will not flatten.
//
// It returns nil when neither has been reported as false, so the advice is present only when there
// is a measured finding behind it -- a null must never produce a remedy.
func restrictionAdvice(s *store.DeviceState) map[string]any {
	var steps []string
	if s.PowerExempt != nil && !*s.PowerExempt {
		steps = append(steps, "Settings → Apps → FamilyGuard → Battery → Unrestricted "+
			"(on Samsung, also Settings → Battery → Background usage limits: remove FamilyGuard "+
			"from Sleeping apps and Deep sleeping apps)")
	}
	if s.ExactAlarms != nil && !*s.ExactAlarms {
		steps = append(steps, "Settings → Apps → FamilyGuard → Alarms and reminders → allow")
	}
	if len(steps) == 0 {
		return nil
	}
	return map[string]any{
		"restricted": true,
		"effect": "Ring, Lock and Locate can take minutes to arrive while the phone is asleep. " +
			"Nothing reports an error when this happens: the work is late, not lost.",
		"remedy": steps,
		"caveat": "FamilyGuard cannot grant these itself — there is no device-owner API for either.",
	}
}

// add registers one tool, giving every handler the same error and encoding treatment.
//
// An error is returned as an error result rather than as a transport failure, so a model sees what
// went wrong and can correct itself; a transport failure would just end the call.
func add[In any](server *mcp.Server, client *fgclient.Client, name, description string,
	handler func(context.Context, *fgclient.Client, In) (any, error)) {

	mcp.AddTool(server, &mcp.Tool{Name: name, Description: description},
		func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, any, error) {
			value, err := handler(ctx, client, in)
			if err != nil {
				return &mcp.CallToolResult{
					IsError: true,
					Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
				}, nil, nil
			}
			encoded, err := json.MarshalIndent(value, "", "  ")
			if err != nil {
				return &mcp.CallToolResult{
					IsError: true,
					Content: []mcp.Content{&mcp.TextContent{Text: "encoding the result: " + err.Error()}},
				}, nil, nil
			}
			return &mcp.CallToolResult{
				Content: []mcp.Content{&mcp.TextContent{Text: string(encoded)}},
			}, nil, nil
		})
}
