package awsbrowser

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
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
	if e.Field != "" {
		return fmt.Sprintf("AWS profile %q field %q: %s", e.Profile, e.Field, e.Reason)
	}
	return fmt.Sprintf("AWS profile %q: %s", e.Profile, e.Reason)
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
	if !utf8.Valid(data) {
		return nil, profileSourceError("", "", "malformed profile document")
	}

	var current *sharedProfile
	ignoreSection := false
	for lineNumber, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSuffix(raw, "\r")
		if containsForbiddenControl(line) {
			return nil, malformedProfileLine(lineNumber + 1)
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") || len(line) == 2 {
				return nil, malformedProfileLine(lineNumber + 1)
			}
			name, ignored, ok := sharedProfileSectionName(strings.TrimSpace(line[1:len(line)-1]), config)
			if !ok {
				return nil, malformedProfileLine(lineNumber + 1)
			}
			if ignored {
				current = nil
				ignoreSection = true
				continue
			}
			if _, exists := profiles[name]; exists {
				return nil, profileSourceError(name, "", "duplicate profile section")
			}
			profile := sharedProfile{name: name}
			profiles[name] = profile
			current = &profile
			ignoreSection = false
			continue
		}
		if current == nil {
			if ignoreSection {
				continue
			}
			return nil, malformedProfileLine(lineNumber + 1)
		}
		key, value, ok := strings.Cut(line, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		if !ok || key == "" || strings.TrimSpace(value) == "" || containsForbiddenControl(key) {
			return nil, malformedProfileLine(lineNumber + 1)
		}
		if err := setSharedProfileField(current, key, strings.TrimSpace(value)); err != nil {
			return nil, profileSourceError(current.name, key, err.Error())
		}
		profiles[current.name] = *current
	}
	return profiles, nil
}

func sharedProfileSectionName(header string, config bool) (name string, ignored, ok bool) {
	if config && header != "default" {
		if strings.HasPrefix(header, "sso-session ") {
			name = strings.TrimSpace(strings.TrimPrefix(header, "sso-session "))
			return "", true, validProfileName(name)
		}
		if !strings.HasPrefix(header, "profile ") {
			return "", false, false
		}
		header = strings.TrimSpace(strings.TrimPrefix(header, "profile "))
	}
	if !validProfileName(header) {
		return "", false, false
	}
	return header, false, true
}

func setSharedProfileField(profile *sharedProfile, key, value string) error {
	switch key {
	case "aws_access_key_id":
		profile.hasAccessKeyID = true
	case "aws_secret_access_key":
		profile.hasSecretAccessKey = true
	case "credential_process":
		profile.hasCredentialProc = true
	case "role_arn":
		profile.hasRoleARN = true
	case "sso_session", "sso_start_url":
		profile.hasSSO = true
	case "source_profile":
		if !validProfileName(value) {
			return errors.New("invalid source profile name")
		}
		profile.sourceProfile = value
	case "credential_source":
		if containsForbiddenControl(value) {
			return errors.New("invalid credential source")
		}
		profile.credentialSource = value
	}
	return nil
}

func validProfileName(name string) bool {
	if name == "" || containsForbiddenControl(name) {
		return false
	}
	for _, r := range name {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '.' && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func containsForbiddenControl(value string) bool {
	for _, r := range value {
		if r == 0x7f || (r < 0x20 && r != '\t') {
			return true
		}
	}
	return false
}

func malformedProfileLine(line int) error {
	return profileSourceError("", "", fmt.Sprintf("malformed profile document at line %d", line))
}

func profileSourceError(profile, field, reason string) *ProfileSourceError {
	return &ProfileSourceError{Profile: profile, Field: field, Reason: reason}
}
