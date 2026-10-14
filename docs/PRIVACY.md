# FamilyGuard — Privacy Policy

**Effective date:** 2026-10-14
**App:** FamilyGuard (Android package `io.github.helios57.familyguard`)
**Developer / contact:** helios157@gmail.com
**Source code:** https://github.com/helios57/familyguard (MIT licence)

## 1. Who this policy is about, and who holds the data

FamilyGuard is a parental-control app. A parent installs it on their child's Android phone or tablet as
the phone's *device owner* (this requires a factory reset and a provisioning QR code), and the phone then
follows the rules the parent sets in a web console.

FamilyGuard is **self-hosted**. The app talks only to a control-plane server that **the family runs
itself** (its "control plane"), at an address the parent chooses when setting the phone up. The
developer does **not** operate a server for FamilyGuard, does **not** receive any data from your phone,
and has no access to any family's server or database. The family member who runs the server (normally a
parent) decides what is collected, who can see it and how long it is kept, and is responsible for it.

Apart from the family's server, the app itself talks to Google only if the family's server turns on
optional push notifications (section 4), and Android sends the phone's DNS look-ups to the filtering
resolver the parent picks, if the parent picks one (section 4).

There are two builds of the app, from the same source code (section 2a). This policy covers both; where
they differ, it says so.

## 2. What the app sends to the family's own server

Everything below is sent over HTTPS (TLS) to the family's server. A release build refuses plain-HTTP
connections. Nothing is sent anywhere until the phone has been enrolled with a one-time code from that
server.

| Data | What exactly | Why |
|---|---|---|
| **Device facts** | Phone model, Android version, the FamilyGuard version, a server-assigned device ID, the list of packages that must never be blocked (phone, SMS, settings) | Enrolment and showing the parent which phone is which |
| **Device status ("heartbeat")** | Battery level, charging yes/no, screen on/off, Wi-Fi vs. mobile data, the FamilyGuard version, whether the phone has applied the current rules, whether usage access / exact alarms / battery exemption / full-screen alarms are allowed, which apps are home-screen launchers, the reason the last self-update failed (if any; self-hosted build only) | Shows the parent whether the phone is online and actually enforcing the rules |
| **Installed apps** | Package name, app name, whether it is a system app, hidden, suspended, or has a launcher icon | So the parent can allow or block apps |
| **App usage (screen time)** | Per app: minutes in the foreground per day, and the start/end time of each sitting; how much of it was paid from earned "bonus" time | Daily limits, bedtime and the parent's screen-time view |
| **Location** | Latitude, longitude, accuracy and time of the fix | **Only** when a parent presses *Locate* (one fix), or during a *Live* session a parent starts (one position every 10 seconds, for at most 2 hours). The app does not track location at any other time. |
| **Child's requests and task reports** | "Ask for more time": 15/30/60 minutes and an optional note of up to 140 characters typed by the child; "task done" taps on the daily plan | So the parent can answer the request or approve the task |
| **Command results** | Whether a parent's command (lock, ring, locate, update…) succeeded, and why not | So the parent sees what actually happened |
| **Recovery events** | When the offline recovery code was entered on the phone and whether it was correct | Security audit for the parent |
| **Energy report** | CPU time, network bytes, number of wake-ups and connections, and time spent in each power mode, used by FamilyGuard itself | Shows the battery cost of the app |
| **Push address** (optional) | The phone's Firebase Installation ID, if push is enabled on the server | So the server can wake the phone (section 4) |
| **Remote debugging** (optional; **self-hosted build only** — not in the Google Play build) | If, and only if, the parent has turned on *Allow debugging* for the child and opens a debugging session, the phone connects its own Android debug bridge (adb) through the server to the parent's computer. The connection is encrypted end-to-end between the parent's adb and the phone; the server relays it and cannot read it. The phone shows a notification ("A parent is connected for debugging") for as long as a session is open. | Fixing problems on the phone |
| **Ad-filter status** (self-hosted build only) | Whether the on-phone ad filter is running, why not if it is not, how many rules its list has and when the list was fetched | So the parent can see whether the filter works |

The app does **not** collect: contacts, call logs, SMS or messages, photos, videos, files, microphone or
camera input, browsing history, keystrokes, screen contents, passwords, advertising IDs, IMEI or
serial numbers, or payment information. It contains **no advertising and no analytics SDK**.

## 2a. The two builds

- **The Google Play build** (installed from Google Play) does everything above except remote debugging.
  It has no ad filter, never updates itself (Google Play does that), and shows a visible
  "Connected to the family settings" notification with a status-bar icon for as long as it runs.
- **The self-hosted build** (the APK the family's own server hands out when a phone is set up) adds
  three things:
  - **Remote debugging**, as described in the table above.
  - **Self-update.** It downloads new versions of itself from the family's server and installs them
    after checking their checksum and signature.
  - **An on-phone ad and tracker filter**, if the parent switches it on. It runs as a local VPN on the
    phone: it reads the destination name of the phone's connections (the DNS name, the TLS server
    name or the HTTP `Host:` header) **on the phone**, to block advertising and trackers. It does not
    record or send these names or any traffic content anywhere; the server only receives the status
    line in the table above. The filter list is downloaded over HTTPS from an address the parent sets,
    so that list's host sees the phone's IP address when it is fetched.

## 3. What the app receives from the family's server

The rules (bedtime, limits, blocked apps, content-filter resolver), the child's daily plan, the alarm
clock times, and the child's agenda for today and tomorrow. If a parent has linked a calendar, the
**server** (not the phone) reads that calendar's iCalendar address; the phone only receives the
resulting entries.

## 4. Google services the app uses

- **Firebase Cloud Messaging (optional).** If the family's server is configured with its own Firebase
  project, the phone registers with Google's Firebase service and receives "wake-up" messages. The
  message contains only `{"t":"sync"}` — no name, no command, no location. Google therefore learns that
  this phone was woken and when, plus the technical data the Firebase SDK itself sends to Google
  (Firebase Installation ID, app and device information). Google's privacy policy applies to that data:
  https://policies.google.com/privacy. Without push configured, the app does not contact Firebase.
- **Google Sign-In (parents only, in the web console).** Parents sign in to the family's web console with
  their Google account. This happens in the parent's browser, not in the app on the child's phone. The
  family's server receives the parent's e-mail address from Google to check that the parent is allowed.
- **Google Play** installs and updates the app when it was installed from Google Play.

**The DNS resolver the parent chooses.** If the parent sets a content-filtering resolver for the child,
the app sets it as the phone's Android *Private DNS*. Android then sends the phone's DNS look-ups (the
names of the sites and services the phone connects to) to that resolver, which is run by the provider
the parent chose and is covered by that provider's privacy policy. FamilyGuard does not receive or store
these look-ups.

## 5. Data stored on the phone

Credentials, the recovery code material, the current rules and measured screen time are stored on the
phone in files encrypted with a key held in the Android Keystore. Android backup of the app's data is
disabled, so it is not copied to a cloud backup.

## 6. Who can see the data

Only people the family's server lets in: the parents (and guardians) the family has invited, according
to their role, and whoever administers that server. The developer cannot see it. No data is sold or
shared for advertising.

## 7. How long data is kept

The family's server decides. With the default settings it deletes location history after 365 days
(`LOCATION_RETENTION_DAYS`) and its audit log after 365 days (`AUDIT_RETENTION_DAYS`). Other records are
kept until a parent deletes them.

## 8. Deleting data

- A parent can delete a phone or a child in the web console. Deleting a phone deletes its location
  history, usage, installed-app list, status and recovery events on the family's server; deleting a
  child deletes the child's profile and everything attached to it, including its phones and its
  requests for more time. Entries in the server's audit log are kept until the audit retention
  period ends (section 7).
- Factory-resetting the phone removes FamilyGuard and all data it stored on the phone. FamilyGuard can
  never block a factory reset.
- Because the developer holds no data, a deletion request must go to the person who runs your family's
  server. If you do not know who that is, or you have a question about this policy, write to
  helios157@gmail.com.

## 9. Children

FamilyGuard is a tool for parents. It is installed on a child's phone by the parent, and the child's
data goes only to the parent's own server. The phone always shows that it is managed: a persistent
"Connected to the family settings" notification while FamilyGuard runs (in the Google Play build with
an icon in the status bar and on the lock screen), the FamilyGuard icon in the
app list, Android's own "this device is managed" notice, and Android's own location icon while
location is in use (on devices that show one). FamilyGuard is not designed for, and must not be used
for, secretly monitoring another adult.

## 10. Security

All traffic between the phone and the family's server uses TLS. Each phone has its own credential, the
server only lets a phone read and write its own records, and each phone has its own offline recovery
code. Security details and how to report a vulnerability: `SECURITY.md` in the source repository.

## 11. Changes

Changes to this policy are published in the source repository and at the URL where you found it, with a
new effective date.

## 12. Contact

helios157@gmail.com
