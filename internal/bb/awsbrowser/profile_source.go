package awsbrowser

import (
	"fmt"
	"strings"
)

// ProfileSourceKind identifies the credential mechanism selected by a shared
// AWS profile. It deliberately describes configuration only; it never carries
// credentials or command text.
type ProfileSourceKind string

const (
	ProfileSourceStatic            ProfileSourceKind = "static"
	ProfileSourceSSO               ProfileSourceKind = "sso"
	ProfileSourceRole              ProfileSourceKind = "role"
	ProfileSourceCredentialProcess ProfileSourceKind = "credential_process"
)

// ProfileSource describes a named profile and the profiles traversed while
// resolving it. Chain is ordered from the requested profile to its terminal
// source. It contains profile names only.
type ProfileSource struct {
	Kind    ProfileSourceKind
	Profile string
	Chain   []string
}

// ProfileSourceError reports invalid or unsupported profile configuration.
// It is intentionally value-free: errors identify profiles and field names,
// but never include configuration values or credential material.
type ProfileSourceError struct {
	Profile string
	Field   string
	Reason  string
}

func (e *ProfileSourceError) Error() string {
	if e == nil {
		return ""
	}
	profile := "requested"
	if validProfileName(e.Profile) {
		profile = fmt.Sprintf("%q", e.Profile)
	}
	reason := safeProfileSourceReason(e.Reason)
	if field := safeProfileSourceField(e.Field); field != "" {
		return fmt.Sprintf("AWS profile %s field %q: %s", profile, field, reason)
	}
	return fmt.Sprintf("AWS profile %s: %s", profile, reason)
}

func safeProfileSourceField(field string) string {
	switch field {
	case "aws_access_key_id", "aws_secret_access_key", "credential_process", "role_arn",
		"sso_session", "sso_start_url", "source_profile", "credential_source":
		return field
	default:
		return ""
	}
}

func safeProfileSourceReason(reason string) string {
	switch reason {
	case "invalid profile name", "profile not found", "Environment is not allowed for named profiles",
		"unsupported credential source", "requires source_profile", "invalid source profile name",
		"requires role_arn", "incomplete static credentials", "no supported credential source",
		"invalid field value", "malformed profile document":
		return reason
	}
	if strings.HasPrefix(reason, "cycle in source_profile chain:") {
		return "cycle in source_profile chain"
	}
	return "invalid profile configuration"
}

// ClassifyProfileSource parses AWS shared config and credentials documents for
// a named profile. It only retains the non-secret values needed to follow
// source_profile references; credential values and credential_process command
// text are never returned or included in errors.
func ClassifyProfileSource(profile string, config, credentials []byte) (ProfileSource, error) {
	if !validProfileName(profile) {
		return ProfileSource{}, profileSourceError(profile, "", "invalid profile name")
	}

	configProfiles, err := parseSharedProfileDocument(config, true)
	if err != nil {
		return ProfileSource{}, err
	}
	credentialProfiles, err := parseSharedProfileDocument(credentials, false)
	if err != nil {
		return ProfileSource{}, err
	}

	profiles := mergeSharedProfiles(configProfiles, credentialProfiles)
	chain, kind, err := classifySharedProfile(profile, profiles, nil, nil)
	if err != nil {
		return ProfileSource{}, err
	}
	return ProfileSource{Kind: kind, Profile: profile, Chain: chain}, nil
}

type sharedProfile struct {
	name string

	hasAccessKeyID     bool
	hasSecretAccessKey bool
	hasCredentialProc  bool
	hasRoleARN         bool
	hasSSO             bool
	invalidField       string

	sourceProfile    string
	credentialSource string
}

func mergeSharedProfiles(config, credentials map[string]sharedProfile) map[string]sharedProfile {
	out := make(map[string]sharedProfile, len(config)+len(credentials))
	for name, profile := range config {
		out[name] = profile
	}
	for name, credentialsProfile := range credentials {
		profile, ok := out[name]
		if !ok {
			out[name] = credentialsProfile
			continue
		}
		profile.hasAccessKeyID = profile.hasAccessKeyID || credentialsProfile.hasAccessKeyID
		profile.hasSecretAccessKey = profile.hasSecretAccessKey || credentialsProfile.hasSecretAccessKey
		profile.hasCredentialProc = profile.hasCredentialProc || credentialsProfile.hasCredentialProc
		profile.hasRoleARN = profile.hasRoleARN || credentialsProfile.hasRoleARN
		profile.hasSSO = profile.hasSSO || credentialsProfile.hasSSO
		if profile.invalidField == "" {
			profile.invalidField = credentialsProfile.invalidField
		}
		if profile.sourceProfile == "" {
			profile.sourceProfile = credentialsProfile.sourceProfile
		}
		if profile.credentialSource == "" {
			profile.credentialSource = credentialsProfile.credentialSource
		}
		out[name] = profile
	}
	return out
}

func classifySharedProfile(name string, profiles map[string]sharedProfile, visiting, chain []string) ([]string, ProfileSourceKind, error) {
	for _, current := range visiting {
		if current == name {
			cycle := append(append([]string(nil), chain...), name)
			return nil, "", profileSourceError(name, "source_profile", "cycle in source_profile chain: "+strings.Join(cycle, " -> "))
		}
	}

	profile, ok := profiles[name]
	if !ok {
		return nil, "", profileSourceError(name, "", "profile not found")
	}
	chain = append(chain, name)
	visiting = append(visiting, name)

	if profile.invalidField != "" {
		return nil, "", profileSourceError(name, profile.invalidField, "invalid field value")
	}
	if profile.credentialSource != "" {
		if strings.EqualFold(profile.credentialSource, "Environment") {
			return nil, "", profileSourceError(name, "credential_source", "Environment is not allowed for named profiles")
		}
		return nil, "", profileSourceError(name, "credential_source", "unsupported credential source")
	}
	if profile.hasRoleARN {
		if profile.sourceProfile == "" {
			return nil, "", profileSourceError(name, "role_arn", "requires source_profile")
		}
		if !validProfileName(profile.sourceProfile) {
			return nil, "", profileSourceError(name, "source_profile", "invalid source profile name")
		}
		childChain, _, err := classifySharedProfile(profile.sourceProfile, profiles, visiting, chain)
		if err != nil {
			return nil, "", err
		}
		return childChain, ProfileSourceRole, nil
	}
	if profile.sourceProfile != "" {
		return nil, "", profileSourceError(name, "source_profile", "requires role_arn")
	}
	if profile.hasCredentialProc {
		return chain, ProfileSourceCredentialProcess, nil
	}
	if profile.hasSSO {
		return chain, ProfileSourceSSO, nil
	}
	if profile.hasAccessKeyID && profile.hasSecretAccessKey {
		return chain, ProfileSourceStatic, nil
	}
	if profile.hasAccessKeyID || profile.hasSecretAccessKey {
		return nil, "", profileSourceError(name, "aws_access_key_id", "incomplete static credentials")
	}
	return nil, "", profileSourceError(name, "", "no supported credential source")
}

func parseSharedProfileDocument(data []byte, config bool) (map[string]sharedProfile, error) {
	profiles := make(map[string]sharedProfile)
	if len(data) == 0 {
		return profiles, nil
	}
	var current *sharedProfile
	var currentKey string
	var currentValueEmpty bool
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSuffix(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if header, ok := sharedProfileHeader(line); ok {
			name, ignored := sharedProfileSectionName(header, config)
			current = nil
			currentKey = ""
			currentValueEmpty = false
			if ignored || !validProfileName(name) {
				continue
			}
			profile := profiles[name]
			profile.name = name
			profiles[name] = profile
			current = &profile
			continue
		}
		if current == nil {
			continue
		}

		indented := len(line) > 0 && (line[0] == ' ' || line[0] == '\t')
		key, value, ok := splitSharedProfileProperty(line)
		if !ok {
			continue
		}
		if indented && currentKey != "" && currentValueEmpty {
			// A nested setting belongs to the preceding map-valued property.
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(trimSharedProfilePropertyComment(value))
		value = unquoteSharedProfileValue(value)
		currentKey = key
		currentValueEmpty = value == ""
		setSharedProfileField(current, key, value)
		profiles[current.name] = *current
	}
	return profiles, nil
}

func sharedProfileHeader(line string) (string, bool) {
	header, _, _ := strings.Cut(line, "#")
	header, _, _ = strings.Cut(header, ";")
	header = strings.TrimSpace(header)
	if len(header) < 2 || header[0] != '[' || header[len(header)-1] != ']' {
		return "", false
	}
	return strings.TrimSpace(header[1 : len(header)-1]), true
}

func sharedProfileSectionName(header string, config bool) (name string, ignored bool) {
	if config {
		if header == "default" {
			return header, false
		}
		if !strings.HasPrefix(header, "profile ") {
			return "", true
		}
		return strings.TrimSpace(strings.TrimPrefix(header, "profile ")), false
	}
	if strings.HasPrefix(header, "profile ") {
		return "", true
	}
	return header, false
}

func splitSharedProfileProperty(line string) (key, value string, ok bool) {
	line = strings.TrimLeft(line, " \t")
	equals := strings.IndexByte(line, '=')
	colon := strings.IndexByte(line, ':')
	separator := equals
	if separator < 0 || colon >= 0 && colon < separator {
		separator = colon
	}
	if separator < 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:separator])
	if key == "" || containsForbiddenControl(key) {
		return "", "", false
	}
	return key, line[separator+1:], true
}

func trimSharedProfilePropertyComment(value string) string {
	for _, marker := range []string{" #", " ;", "\t#", "\t;"} {
		if before, _, found := strings.Cut(value, marker); found {
			value = before
		}
	}
	return value
}

func unquoteSharedProfileValue(value string) string {
	if len(value) >= 2 && ((value[0] == '\'' && value[len(value)-1] == '\'') ||
		(value[0] == '"' && value[len(value)-1] == '"')) {
		return value[1 : len(value)-1]
	}
	return value
}

func setSharedProfileField(profile *sharedProfile, key, value string) {
	if containsForbiddenControl(value) {
		switch key {
		case "aws_access_key_id", "aws_secret_access_key", "credential_process", "role_arn",
			"sso_session", "sso_start_url", "source_profile", "credential_source":
			profile.invalidField = key
		}
		return
	}
	if profile.invalidField == key {
		profile.invalidField = ""
	}
	switch key {
	case "aws_access_key_id":
		profile.hasAccessKeyID = value != ""
	case "aws_secret_access_key":
		profile.hasSecretAccessKey = value != ""
	case "credential_process":
		profile.hasCredentialProc = value != ""
	case "role_arn":
		profile.hasRoleARN = value != ""
	case "sso_session", "sso_start_url":
		profile.hasSSO = value != ""
	case "source_profile":
		profile.sourceProfile = value
	case "credential_source":
		profile.credentialSource = value
	}
}

func validProfileName(name string) bool {
	return profileNameRE.MatchString(name)
}

func containsForbiddenControl(value string) bool {
	for _, r := range value {
		if r == 0x7f || (r < 0x20 && r != '\t') {
			return true
		}
	}
	return false
}

func profileSourceError(profile, field, reason string) *ProfileSourceError {
	return &ProfileSourceError{Profile: profile, Field: field, Reason: reason}
}
