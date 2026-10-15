package bb

// bb sec verify asks the issuing service who a token belongs to and, where the
// service says, when it expires. Only the identity, status and expiry come
// back; response bodies and the token never reach output or errors.
//
// The verify target decides where a token is sent. Setting it requires a
// terminal, and each target carries an HMAC keyed from the age identity: the
// metadata file is plaintext, so a target written there by anything other than
// "bb sec meta" (an agent's file tools, a script) is refused at use time. Only
// someone who can read the identity, and so every value anyway, can forge one.

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

const (
	verifyTimeout      = 10 * time.Second
	verifyMaxBodyBytes = 1 << 20
	defaultGitHubAPI   = "https://api.github.com"
)

// secretVerify says how to check a field: which kind of service and where.
// UserField names the field of the same service holding the user name, for
// services that authenticate with user and token together.
type secretVerify struct {
	Kind      string `json:"kind"`
	URL       string `json:"url,omitempty"`
	UserField string `json:"user_field,omitempty"`
	MAC       string `json:"mac,omitempty"`
}

// verifyTargetKey derives the signing key for verify targets from the age
// identity file.
func (a *App) verifyTargetKey() ([]byte, error) {
	_, identity := a.secPaths()
	content, err := os.ReadFile(identity)
	if err != nil {
		return nil, fmt.Errorf("read age identity to sign the verify target: %w", err)
	}
	sum := sha256.Sum256(append([]byte("bb sec verify target v1\x00"), content...))
	return sum[:], nil
}

func verifyTargetMAC(signingKey []byte, key string, spec secretVerify) string {
	mac := hmac.New(sha256.New, signingKey)
	mac.Write([]byte(strings.Join([]string{key, spec.Kind, spec.URL, spec.UserField}, "\x00")))
	return hex.EncodeToString(mac.Sum(nil))
}

// trustedVerifyTarget re-validates a stored target and checks its signature.
func trustedVerifyTarget(signingKey []byte, key string, spec secretVerify) error {
	if _, err := parseSecretVerify(spec.Kind+"="+spec.URL, spec.UserField); err != nil {
		return fmt.Errorf("invalid verify target")
	}
	want := verifyTargetMAC(signingKey, key, spec)
	if !hmac.Equal([]byte(want), []byte(spec.MAC)) {
		return fmt.Errorf("verify target was not set by bb sec meta (or the field was renamed); set it again with: bb sec meta %s --verify", key)
	}
	return nil
}

var verifyKinds = map[string]bool{"gitlab": true, "github": true, "jenkins": true, "awx": true}

// parseSecretVerify reads KIND or KIND=URL; "none" clears.
func parseSecretVerify(value, userField string) (*secretVerify, error) {
	if value == "none" {
		return nil, nil
	}
	kind, rawURL, _ := strings.Cut(value, "=")
	if !verifyKinds[kind] {
		return nil, invalid("--verify kind must be gitlab, github, jenkins or awx, optionally =URL")
	}
	spec := &secretVerify{Kind: kind, URL: strings.TrimRight(rawURL, "/"), UserField: userField}
	if spec.URL == "" && kind == "github" {
		spec.URL = defaultGitHubAPI
	}
	if spec.URL == "" {
		return nil, invalid("--verify " + kind + " needs the service URL: " + kind + "=https://host")
	}
	if err := validVerifyURL(spec.URL); err != nil {
		return nil, err
	}
	if kind == "jenkins" && userField == "" {
		return nil, invalid("--verify jenkins needs --verify-user <field> naming the user name field")
	}
	if userField != "" && !validSecretName(userField) {
		return nil, invalid("--verify-user must be a field name")
	}
	return spec, nil
}

// validVerifyURL requires https so a token never crosses the network in clear;
// plain http is accepted for loopback only.
func validVerifyURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return invalid("verify URL must be a plain https://host[/path] URL")
	}
	if u.Scheme == "https" {
		return nil
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); u.Scheme == "http" && (host == "localhost" || (ip != nil && ip.IsLoopback())) {
		return nil
	}
	return invalid("verify URL must use https")
}

func defaultVerifyHTTP(request *http.Request) (*http.Response, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// An environment-chosen proxy would see the token's request.
	transport.Proxy = nil
	client := &http.Client{
		Transport: transport,
		Timeout:   verifyTimeout,
		// A redirect could carry a custom token header to another host.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return client.Do(request)
}

type secretVerifyResult struct {
	Key       string `json:"key"`
	Kind      string `json:"kind"`
	OK        bool   `json:"ok"`
	Status    int    `json:"status,omitempty"`
	Identity  string `json:"identity,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
	Error     string `json:"error,omitempty"`
}

// verifyGet sends one GET and decodes a JSON body into v. Errors name the
// status only.
func (a *App) verifyGet(ctx context.Context, endpoint string, header http.Header, v any) (int, http.Header, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return 0, nil, fmt.Errorf("build request")
	}
	for name, values := range header {
		request.Header[name] = values
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "bb-sec-verify")
	response, err := a.httpDo(request)
	if err != nil {
		// Transport errors name the URL, never a header.
		return 0, nil, fmt.Errorf("request failed: %s", safeTerminalText(err.Error()))
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, verifyMaxBodyBytes))
	if err != nil {
		return response.StatusCode, response.Header, fmt.Errorf("read response")
	}
	if response.StatusCode < 200 || response.StatusCode > 299 {
		return response.StatusCode, response.Header, fmt.Errorf("HTTP %d", response.StatusCode)
	}
	if v != nil && json.Unmarshal(body, v) != nil {
		return response.StatusCode, response.Header, fmt.Errorf("unexpected response body")
	}
	return response.StatusCode, response.Header, nil
}

func (a *App) verifySecret(key string, spec secretVerify, token, user string) secretVerifyResult {
	result := secretVerifyResult{Key: key, Kind: spec.Kind}
	ctx, cancel := context.WithTimeout(context.Background(), verifyTimeout)
	defer cancel()
	var err error
	switch spec.Kind {
	case "gitlab":
		header := http.Header{"Private-Token": {token}}
		var me struct {
			Username string `json:"username"`
		}
		result.Status, _, err = a.verifyGet(ctx, spec.URL+"/api/v4/user", header, &me)
		result.Identity = me.Username
		if err == nil {
			// Personal, project and group tokens report their own expiry; other
			// credentials do not have this endpoint, which is not a failure.
			var self struct {
				Name      string `json:"name"`
				ExpiresAt string `json:"expires_at"`
			}
			if _, _, selfErr := a.verifyGet(ctx, spec.URL+"/api/v4/personal_access_tokens/self", header, &self); selfErr == nil {
				result.ExpiresAt = dateOnly(self.ExpiresAt)
				if self.Name != "" {
					result.Identity += " (token " + self.Name + ")"
				}
			}
		}
	case "github":
		var me struct {
			Login string `json:"login"`
		}
		var header http.Header
		result.Status, header, err = a.verifyGet(ctx, spec.URL+"/user", http.Header{"Authorization": {"Bearer " + token}}, &me)
		result.Identity = me.Login
		if header != nil {
			result.ExpiresAt = dateOnly(header.Get("GitHub-Authentication-Token-Expiration"))
		}
	case "jenkins":
		request := http.Header{"Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+token))}}
		var me struct {
			ID string `json:"id"`
		}
		result.Status, _, err = a.verifyGet(ctx, spec.URL+"/me/api/json", request, &me)
		result.Identity = me.ID
		if err == nil && me.ID == "anonymous" {
			err = fmt.Errorf("Jenkins treated the credential as anonymous")
		}
	case "awx":
		var me struct {
			Results []struct {
				Username string `json:"username"`
			} `json:"results"`
		}
		result.Status, _, err = a.verifyGet(ctx, spec.URL+"/api/v2/me/", http.Header{"Authorization": {"Bearer " + token}}, &me)
		if len(me.Results) > 0 {
			result.Identity = me.Results[0].Username
		}
	default:
		err = fmt.Errorf("unknown verify kind")
	}
	if err == nil && result.Identity == "" {
		err = fmt.Errorf("the response named no user")
	}
	if err != nil {
		result.Error = err.Error()
		result.Identity, result.ExpiresAt = "", ""
		return result
	}
	result.OK = true
	return result
}

// dateOnly keeps the YYYY-MM-DD part of a timestamp a service returned.
func dateOnly(value string) string {
	value = strings.TrimSpace(value)
	if len(value) < 10 {
		return ""
	}
	if _, err := time.Parse(secretDateLayout, value[:10]); err != nil {
		return ""
	}
	return value[:10]
}

// runSecretVerification verifies every field that has a verify target, limited
// to service (and field) when given, and records expiry the service reported.
func (a *App) runSecretVerification(service, field string) ([]secretVerifyResult, error) {
	meta, err := a.readSecretMeta()
	if err != nil {
		return nil, err
	}
	keys := []string{}
	for key, entry := range meta {
		if entry.Verify == nil {
			continue
		}
		s, f, _ := strings.Cut(key, "/")
		if service != "" && (s != service || (field != "" && f != field)) {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		if field != "" {
			return nil, invalid("no verify target for " + secretKey(service, field) + "; set one with: bb sec meta " + secretKey(service, field) + " --verify gitlab=https://host")
		}
		return []secretVerifyResult{}, nil
	}
	data, err := a.readSecrets()
	if err != nil {
		return nil, err
	}
	signingKey, err := a.verifyTargetKey()
	if err != nil {
		return nil, err
	}
	results := make([]secretVerifyResult, 0, len(keys))
	learned := map[string]string{}
	for _, key := range keys {
		spec := *meta[key].Verify
		if err := trustedVerifyTarget(signingKey, key, spec); err != nil {
			results = append(results, secretVerifyResult{Key: key, Kind: spec.Kind, Error: err.Error()})
			continue
		}
		s, f, _ := strings.Cut(key, "/")
		token, ok := data[s][f]
		if !ok {
			results = append(results, secretVerifyResult{Key: key, Kind: spec.Kind, Error: "field not in the store"})
			continue
		}
		user := ""
		if spec.UserField != "" {
			if user, ok = data[s][spec.UserField]; !ok {
				results = append(results, secretVerifyResult{Key: key, Kind: spec.Kind, Error: "user field " + spec.UserField + " not in the store"})
				continue
			}
		}
		result := a.verifySecret(key, spec, token, user)
		results = append(results, result)
		if result.ExpiresAt != "" && result.ExpiresAt != meta[key].ExpiresAt {
			learned[key] = result.ExpiresAt
		}
	}
	// Verification is not a use: a token checked weekly may still be unused.
	if len(learned) > 0 {
		if err := a.updateSecretMeta(func(entries map[string]secretMeta) error {
			for key, date := range learned {
				entry := entries[key]
				entry.ExpiresAt, entry.ExpiresSource = date, "verify"
				entries[key] = entry
			}
			return nil
		}); err != nil {
			fmt.Fprintln(a.err, "Verified, but could not record the reported expiry: "+err.Error())
		}
	}
	return results, nil
}

func (a *App) secVerify(args []string) error {
	args, jsonMode := takeFlag(args, "--json")
	if len(args) > 2 || (len(args) > 0 && !validSecretName(args[0])) || (len(args) == 2 && !validSecretName(args[1])) {
		return usage("sec verify", "[service[/field]] [--json]")
	}
	service, field := "", ""
	if len(args) > 0 {
		service = args[0]
	}
	if len(args) == 2 {
		field = args[1]
	}
	results, err := a.runSecretVerification(service, field)
	if err != nil {
		return err
	}
	failed := 0
	for _, result := range results {
		if !result.OK {
			failed++
		}
	}
	var outcome error
	if failed > 0 {
		outcome = &CommandError{Code: "secrets_unverified", Message: fmt.Sprintf("%d secret(s) failed verification", failed), Exit: ExitOperational, Reported: jsonMode}
	}
	if jsonMode {
		if err := printEnvelope(a.out, results, nil); err != nil {
			return err
		}
		return outcome
	}
	if len(results) == 0 {
		fmt.Fprintln(a.err, "No field has a verify target; set one with: bb sec meta <service/field> --verify gitlab=https://host")
		return nil
	}
	w := tabwriter.NewWriter(a.out, 0, 4, 2, ' ', 0)
	for _, result := range results {
		state := "ok"
		detail := result.Identity
		if !result.OK {
			state, detail = "FAIL", result.Error
		}
		if result.ExpiresAt != "" {
			detail += "  expires " + result.ExpiresAt
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", safeTerminalText(result.Key), result.Kind, state, safeTerminalText(detail))
	}
	if err := w.Flush(); err != nil {
		return err
	}
	return outcome
}
