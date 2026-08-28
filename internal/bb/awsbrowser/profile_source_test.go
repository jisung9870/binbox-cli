package awsbrowser

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestClassifyProfileSource(t *testing.T) {
	tests := []struct {
		name        string
		profile     string
		config      string
		credentials string
		want        ProfileSource
	}{
		{
			name:        "static credentials file profile",
			profile:     "dev",
			credentials: "[dev]\naws_access_key_id = AKIAEXAMPLE\naws_secret_access_key = not-returned\n",
			want:        ProfileSource{Kind: ProfileSourceStatic, Profile: "dev", Chain: []string{"dev"}},
		},
		{
			name:    "sso profile",
			profile: "dev",
			config:  "[profile dev]\nsso_session = engineering\nsso_account_id = 123456789012\nsso_role_name = ReadOnly\n[sso-session engineering]\nsso_start_url = https://example.awsapps.com/start\nsso_region = us-east-1\n",
			want:    ProfileSource{Kind: ProfileSourceSSO, Profile: "dev", Chain: []string{"dev"}},
		},
		{
			name:    "legacy sso profile",
			profile: "dev",
			config:  "[profile dev]\nsso_start_url = https://example.awsapps.com/start\nsso_region = us-east-1\n",
			want:    ProfileSource{Kind: ProfileSourceSSO, Profile: "dev", Chain: []string{"dev"}},
		},
		{
			name:    "role source chain",
			profile: "admin",
			config:  "[profile admin]\nrole_arn = arn:aws:iam::123456789012:role/Admin\nsource_profile = base\n[profile base]\nsso_session = engineering\n",
			want:    ProfileSource{Kind: ProfileSourceRole, Profile: "admin", Chain: []string{"admin", "base"}},
		},
		{
			name:    "credential process",
			profile: "dev",
			config:  "[profile dev]\ncredential_process = private-command --token very-secret\n",
			want:    ProfileSource{Kind: ProfileSourceCredentialProcess, Profile: "dev", Chain: []string{"dev"}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ClassifyProfileSource(test.profile, []byte(test.config), []byte(test.credentials))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("source=%+v want=%+v", got, test.want)
			}
		})
	}
}

func TestClassifyProfileSourceRejectsEnvironmentCredentialSource(t *testing.T) {
	for _, source := range []string{"Environment", `"Environment"`, "environment"} {
		_, err := ClassifyProfileSource("dev", []byte("[profile dev]\nrole_arn = arn:aws:iam::123456789012:role/ReadOnly\ncredential_source = "+source+"\n"), nil)
		if err == nil || !strings.Contains(err.Error(), "Environment is not allowed") {
			t.Fatalf("source=%q error=%v", source, err)
		}
		if strings.Contains(err.Error(), "arn:aws") {
			t.Fatalf("error leaked configuration value: %q", err)
		}
	}
}

func TestClassifyProfileSourceIgnoresEnvironmentSourceInUnrelatedProfile(t *testing.T) {
	config := "[profile unrelated]\ncredential_source = Environment\n" +
		"[profile dev]\ncredential_process = safe-command\n"
	got, err := ClassifyProfileSource("dev", []byte(config), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != ProfileSourceCredentialProcess {
		t.Fatalf("source=%+v", got)
	}
}

func TestClassifyProfileSourceRejectsInvalidChains(t *testing.T) {
	tests := []struct {
		name    string
		profile string
		config  string
		want    string
	}{
		{
			name:    "missing source",
			profile: "dev",
			config:  "[profile dev]\nrole_arn = arn:aws:iam::123456789012:role/ReadOnly\nsource_profile = missing\n",
			want:    "profile not found",
		},
		{
			name:    "cycle",
			profile: "one",
			config:  "[profile one]\nrole_arn = arn:aws:iam::123456789012:role/One\nsource_profile = two\n[profile two]\nrole_arn = arn:aws:iam::123456789012:role/Two\nsource_profile = one\n",
			want:    "cycle in source_profile chain",
		},
		{
			name:    "role without source profile",
			profile: "dev",
			config:  "[profile dev]\nrole_arn = arn:aws:iam::123456789012:role/ReadOnly\n",
			want:    "requires source_profile",
		},
		{
			name:    "source profile without role",
			profile: "dev",
			config:  "[profile dev]\nsource_profile = base\n",
			want:    "requires role_arn",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ClassifyProfileSource(test.profile, []byte(test.config), nil)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v want substring %q", err, test.want)
			}
		})
	}
}

func TestClassifyProfileSourceToleratesAWSCompatibleUnrelatedContent(t *testing.T) {
	config := "global_property = ignored\n" +
		"[services custom-endpoints]\n" +
		"s3 =\n" +
		"  endpoint_url = http://127.0.0.1\n" +
		"  malformed nested setting\n" +
		"[profile unrelated]\n" +
		"source_profile = contains spaces\n" +
		"credential_process = command\x00secret\n" +
		"[broken unrelated header\n" +
		"this line is also ignored\n" +
		"[profile dev]\n" +
		"credential_process = safe-command\n"

	got, err := ClassifyProfileSource("dev", []byte(config), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != ProfileSourceCredentialProcess || !reflect.DeepEqual(got.Chain, []string{"dev"}) {
		t.Fatalf("source=%+v", got)
	}
}

func TestClassifyProfileSourceRejectsRelevantControlCharacterValues(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{name: "control character", data: "[profile dev]\ncredential_process = command\x00secret\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ClassifyProfileSource("dev", []byte(test.data), nil)
			if err == nil || !strings.Contains(err.Error(), "invalid field value") {
				t.Fatalf("error=%v", err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("error leaked input value: %q", err)
			}
		})
	}
}

func TestProfileNamePolicyIsConsistent(t *testing.T) {
	valid := []string{"default", "dev-1", "prod_2", "team.example", "LG-PAK/common-ops-policy"}
	invalid := []string{"", ".hidden", "-flag", "_private", "contains space", "line\nbreak"}
	for _, profile := range valid {
		if !validProfileName(profile) || !profileNameRE.MatchString(profile) {
			t.Fatalf("valid profile %q rejected", profile)
		}
	}
	for _, profile := range invalid {
		if validProfileName(profile) || profileNameRE.MatchString(profile) {
			t.Fatalf("invalid profile %q accepted", profile)
		}
	}
}

func TestClassifyProfileSourceErrorsAreTypedAndValueFree(t *testing.T) {
	_, err := ClassifyProfileSource("dev", []byte("[profile dev]\naws_access_key_id = AKIAEXAMPLE\n"), nil)
	var sourceErr *ProfileSourceError
	if !errors.As(err, &sourceErr) {
		t.Fatalf("error type=%T want *ProfileSourceError", err)
	}
	if strings.Contains(err.Error(), "AKIAEXAMPLE") {
		t.Fatalf("error leaked credential value: %q", err)
	}
}

func TestProfileSourceErrorRejectsHostileRenderableFields(t *testing.T) {
	const secret = "credential-super-secret"
	err := (&ProfileSourceError{Profile: "line\nbreak", Field: secret, Reason: secret}).Error()
	if strings.Contains(err, secret) || strings.Contains(err, "line") {
		t.Fatalf("error leaked hostile fields: %q", err)
	}
}
