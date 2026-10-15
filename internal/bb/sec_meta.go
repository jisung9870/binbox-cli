package bb

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
)

// secretMeta describes a secret field without containing any part of its value.
// It lives in a plaintext sidecar so expiry checks never need the age key.
type secretMeta struct {
	ExpiresAt     string `json:"expires_at,omitempty"`
	ExpiresSource string `json:"expires_source,omitempty"`
	URL           string `json:"url,omitempty"`
	Note          string `json:"note,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
}

type secretMetaFile struct {
	SchemaVersion int                   `json:"schema_version"`
	Entries       map[string]secretMeta `json:"entries"`
}

const (
	secretDateLayout        = "2006-01-02"
	defaultExpiryWindowDays = 14
)

var relativeExpiryRE = regexp.MustCompile(`^([0-9]{1,4})([dwmy])$`)

func secretKey(service, field string) string { return service + "/" + field }

func (a *App) secMetaPath() string {
	store, _ := a.secPaths()
	return filepath.Join(filepath.Dir(store), "secrets.meta.json")
}

func (a *App) readSecretMeta() (map[string]secretMeta, error) {
	var file secretMetaFile
	if e := readJSON(a.secMetaPath(), &file); e != nil {
		return nil, fmt.Errorf("read secret metadata: %w", e)
	}
	if file.Entries == nil {
		file.Entries = map[string]secretMeta{}
	}
	return file.Entries, nil
}

// updateSecretMeta applies fn to the metadata under the sidecar lock.
func (a *App) updateSecretMeta(fn func(map[string]secretMeta) error) error {
	path := a.secMetaPath()
	return withFileLock(path, func() error {
		entries, e := a.readSecretMeta()
		if e != nil {
			return e
		}
		if e := fn(entries); e != nil {
			return e
		}
		return writeJSONAtomic(path, secretMetaFile{SchemaVersion: 1, Entries: entries})
	})
}

// secretToday is the operator's local calendar date expressed in UTC so that
// date arithmetic against stored YYYY-MM-DD values counts whole days.
func (a *App) secretToday() time.Time {
	y, m, d := a.now().Local().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// parseSecretExpiry accepts YYYY-MM-DD or a relative 30d/12w/6m/1y duration.
// "none" clears the expiry and returns an empty string.
func parseSecretExpiry(value string, today time.Time) (string, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "none" || value == "" {
		return "", nil
	}
	if m := relativeExpiryRE.FindStringSubmatch(value); m != nil {
		n, _ := strconv.Atoi(m[1])
		var t time.Time
		switch m[2] {
		case "d":
			t = today.AddDate(0, 0, n)
		case "w":
			t = today.AddDate(0, 0, 7*n)
		case "m":
			t = today.AddDate(0, n, 0)
		case "y":
			t = today.AddDate(n, 0, 0)
		}
		return t.Format(secretDateLayout), nil
	}
	t, e := time.Parse(secretDateLayout, value)
	if e != nil {
		return "", invalid("expiry must be YYYY-MM-DD, a duration like 90d/12w/6m/1y, or none")
	}
	return t.Format(secretDateLayout), nil
}

// jwtExpiry reads the exp claim of a JWT-shaped value without verifying it.
// Only the expiry date leaves this function.
func jwtExpiry(value string) (string, bool) {
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) != 3 || parts[1] == "" {
		return "", false
	}
	payload, e := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if e != nil {
		return "", false
	}
	var claims struct {
		Exp json.Number `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == "" {
		return "", false
	}
	exp, e := claims.Exp.Float64()
	if e != nil || exp <= 0 {
		return "", false
	}
	return time.Unix(int64(exp), 0).UTC().Format(secretDateLayout), true
}

func validSecretURL(value string) error {
	u, e := url.Parse(value)
	if e != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return invalid("url must be an http(s) URL")
	}
	return nil
}

// secretMetaOptions are the metadata flags shared by set/add and meta.
type secretMetaOptions struct {
	expires, url, note          string
	hasExpires, hasURL, hasNote bool
}

func (o secretMetaOptions) any() bool { return o.hasExpires || o.hasURL || o.hasNote }

func takeSecretMetaOptions(args []string) ([]string, secretMetaOptions, error) {
	var o secretMetaOptions
	var e error
	if args, o.expires, o.hasExpires, e = takeOptionValue(args, "--expires"); e != nil {
		return nil, o, e
	}
	if args, o.url, o.hasURL, e = takeOptionValue(args, "--url"); e != nil {
		return nil, o, e
	}
	if args, o.note, o.hasNote, e = takeOptionValue(args, "--note"); e != nil {
		return nil, o, e
	}
	return args, o, nil
}

// takeOptionValue removes "--name value" or "--name=value" from args.
func takeOptionValue(args []string, name string) ([]string, string, bool, error) {
	out := make([]string, 0, len(args))
	value, found := "", false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == name:
			if i+1 >= len(args) {
				return nil, "", false, invalid("missing value for " + name)
			}
			value, found = args[i+1], true
			i++
		case strings.HasPrefix(arg, name+"="):
			value, found = strings.TrimPrefix(arg, name+"="), true
		default:
			out = append(out, arg)
		}
	}
	return out, value, found, nil
}

// apply merges the options into meta. jwtDate is the expiry read from a newly
// stored value and only fills an expiry the operator did not set explicitly.
func (o secretMetaOptions) apply(meta secretMeta, today time.Time, jwtDate string) (secretMeta, error) {
	switch {
	case o.hasExpires:
		date, e := parseSecretExpiry(o.expires, today)
		if e != nil {
			return meta, e
		}
		meta.ExpiresAt, meta.ExpiresSource = date, ""
		if date != "" {
			meta.ExpiresSource = "manual"
		}
	case jwtDate != "":
		meta.ExpiresAt, meta.ExpiresSource = jwtDate, "jwt"
	}
	if o.hasURL {
		if o.url == "none" || o.url == "" {
			meta.URL = ""
		} else if e := validSecretURL(o.url); e != nil {
			return meta, e
		} else {
			meta.URL = o.url
		}
	}
	if o.hasNote {
		if o.note == "none" {
			meta.Note = ""
		} else {
			meta.Note = o.note
		}
	}
	return meta, nil
}

type secretExpiryStatus struct {
	Key       string `json:"key"`
	ExpiresAt string `json:"expires_at,omitempty"`
	DaysLeft  *int   `json:"days_left,omitempty"`
	State     string `json:"state"`
}

// expiryStatus classifies one entry: unknown, ok, expiring (inside window) or
// expired.
func expiryStatus(key string, meta secretMeta, today time.Time, window int) secretExpiryStatus {
	status := secretExpiryStatus{Key: key, ExpiresAt: meta.ExpiresAt, State: "unknown"}
	if meta.ExpiresAt == "" {
		return status
	}
	t, e := time.Parse(secretDateLayout, meta.ExpiresAt)
	if e != nil {
		return status
	}
	days := int(t.Sub(today).Hours() / 24)
	status.DaysLeft = &days
	switch {
	case days < 0:
		status.State = "expired"
	case days <= window:
		status.State = "expiring"
	default:
		status.State = "ok"
	}
	return status
}

func (s secretExpiryStatus) badge() string {
	if s.DaysLeft == nil {
		return ""
	}
	switch s.State {
	case "expired":
		return fmt.Sprintf("expired %dd ago", -*s.DaysLeft)
	case "expiring":
		return fmt.Sprintf("D-%d !", *s.DaysLeft)
	default:
		return fmt.Sprintf("D-%d", *s.DaysLeft)
	}
}

var expiryStateRank = map[string]int{"unknown": 0, "ok": 1, "expiring": 2, "expired": 3}

// worstServiceStatus summarizes a service by its most urgent field.
func worstServiceStatus(service string, fields map[string]string, meta map[string]secretMeta, today time.Time) secretExpiryStatus {
	worst := secretExpiryStatus{Key: service, State: "unknown"}
	for field := range fields {
		s := expiryStatus(secretKey(service, field), meta[secretKey(service, field)], today, defaultExpiryWindowDays)
		if s.DaysLeft == nil {
			continue
		}
		if worst.DaysLeft == nil || expiryStateRank[s.State] > expiryStateRank[worst.State] ||
			(s.State == worst.State && *s.DaysLeft < *worst.DaysLeft) {
			worst = secretExpiryStatus{Key: service, ExpiresAt: s.ExpiresAt, DaysLeft: s.DaysLeft, State: s.State}
		}
	}
	return worst
}

// printSecretNames prints one name per line, adding an aligned expiry badge
// only when at least one name has one, so plain listings stay unchanged.
func (a *App) printSecretNames(names []string, badges map[string]string) error {
	hasBadge := false
	for _, name := range names {
		if badges[name] != "" {
			hasBadge = true
			break
		}
	}
	if !hasBadge {
		for _, name := range names {
			if _, e := fmt.Fprintln(a.out, name); e != nil {
				return e
			}
		}
		return nil
	}
	var buf bytes.Buffer
	w := tabwriter.NewWriter(&buf, 0, 4, 2, ' ', 0)
	for _, name := range names {
		fmt.Fprintf(w, "%s\t%s\n", name, badges[name])
	}
	if e := w.Flush(); e != nil {
		return e
	}
	for _, line := range strings.SplitAfter(buf.String(), "\n") {
		if line == "" {
			continue
		}
		if _, e := fmt.Fprintln(a.out, strings.TrimRight(line, " \n")); e != nil {
			return e
		}
	}
	return nil
}

// secretExpiryReport lists expired and expiring entries, plus every dated one
// when all is set, from metadata alone.
func (a *App) secretExpiryReport(window int, all bool) (map[string]secretMeta, []secretExpiryStatus, int, error) {
	meta, e := a.readSecretMeta()
	if e != nil {
		return nil, nil, 0, e
	}
	today := a.secretToday()
	keys := make([]string, 0, len(meta))
	for key := range meta {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	results := []secretExpiryStatus{}
	problems := 0
	for _, key := range keys {
		s := expiryStatus(key, meta[key], today, window)
		if s.State == "expired" || s.State == "expiring" {
			problems++
		} else if !all || s.DaysLeft == nil {
			continue
		}
		results = append(results, s)
	}
	return meta, results, problems, nil
}

// secCheck reports expired and expiring secrets from metadata alone; it never
// decrypts the store, so it is cheap enough for a prompt or a scheduler.
func (a *App) secCheck(args []string) error {
	args, jsonMode := takeFlag(args, "--json")
	args, quiet := takeFlag(args, "--quiet")
	args, all := takeFlag(args, "--all")
	args, within, hasWithin, e := takeOptionValue(args, "--within")
	if e != nil {
		return e
	}
	if len(args) != 0 {
		return usage("sec check", "[--within 14d] [--all] [--json|--quiet]")
	}
	window := defaultExpiryWindowDays
	if hasWithin {
		n, convErr := strconv.Atoi(strings.TrimSuffix(within, "d"))
		if convErr != nil || n < 0 {
			return invalid("--within must be a number of days such as 14d")
		}
		window = n
	}
	meta, results, problems, e := a.secretExpiryReport(window, all)
	if e != nil {
		return e
	}
	var result error
	if problems > 0 {
		result = &CommandError{
			Code:     "secrets_expiring",
			Message:  fmt.Sprintf("%d secret(s) expired or expiring within %d days", problems, window),
			Exit:     ExitOperational,
			Reported: quiet || jsonMode,
		}
	}
	switch {
	case quiet:
	case jsonMode:
		if e := printEnvelope(a.out, results, nil); e != nil {
			return e
		}
	default:
		w := tabwriter.NewWriter(a.out, 0, 4, 2, ' ', 0)
		for _, s := range results {
			line := safeTerminalText(s.Key) + "\t" + s.badge() + "\t" + safeTerminalText(s.ExpiresAt)
			if url := meta[s.Key].URL; url != "" && s.State != "ok" {
				line += "\t" + safeTerminalText(url)
			}
			fmt.Fprintln(w, line)
		}
		if e := w.Flush(); e != nil {
			return e
		}
	}
	return result
}

// secMeta shows or edits the metadata of an existing field.
func (a *App) secMeta(args []string) error {
	args, opts, e := takeSecretMetaOptions(args)
	if e != nil {
		return e
	}
	args, jsonMode := takeFlag(args, "--json")
	if len(args) != 2 || !validSecretName(args[0]) || !validSecretName(args[1]) {
		return usage("sec meta", "<service/field> [--expires DATE|90d|none] [--url URL|none] [--note TEXT|none] [--json]")
	}
	data, e := a.readSecrets()
	if e != nil {
		return e
	}
	if _, ok := data[args[0]][args[1]]; !ok {
		return invalid("secret field not found")
	}
	key := secretKey(args[0], args[1])
	if opts.any() {
		e = a.updateSecretMeta(func(entries map[string]secretMeta) error {
			updated, applyErr := opts.apply(entries[key], a.secretToday(), "")
			if applyErr != nil {
				return applyErr
			}
			updated.UpdatedAt = a.now().UTC().Format(time.RFC3339)
			entries[key] = updated
			return nil
		})
		if e != nil {
			return e
		}
	}
	meta, e := a.readSecretMeta()
	if e != nil {
		return e
	}
	m := meta[key]
	if jsonMode {
		return printEnvelope(a.out, struct {
			Key string `json:"key"`
			secretMeta
		}{key, m}, nil)
	}
	status := expiryStatus(key, m, a.secretToday(), defaultExpiryWindowDays)
	w := tabwriter.NewWriter(a.out, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "key\t%s\n", key)
	expires := "-"
	if m.ExpiresAt != "" {
		expires = m.ExpiresAt + " (" + status.badge() + ", " + m.ExpiresSource + ")"
	}
	fmt.Fprintf(w, "expires\t%s\n", expires)
	fmt.Fprintf(w, "url\t%s\n", orDash(safeTerminalText(m.URL)))
	fmt.Fprintf(w, "note\t%s\n", orDash(safeTerminalText(m.Note)))
	return w.Flush()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// renameSecretMeta and dropSecretMeta keep the sidecar aligned with the store.
// A missing sidecar is not an error: metadata is optional.
func (a *App) renameSecretMeta(service, field, newField string) error {
	if _, e := os.Stat(a.secMetaPath()); os.IsNotExist(e) {
		return nil
	}
	return a.updateSecretMeta(func(entries map[string]secretMeta) error {
		if m, ok := entries[secretKey(service, field)]; ok {
			entries[secretKey(service, newField)] = m
			delete(entries, secretKey(service, field))
		}
		return nil
	})
}

func (a *App) dropSecretMeta(keys []string) error {
	if _, e := os.Stat(a.secMetaPath()); os.IsNotExist(e) {
		return nil
	}
	return a.updateSecretMeta(func(entries map[string]secretMeta) error {
		for _, key := range keys {
			delete(entries, key)
		}
		return nil
	})
}
