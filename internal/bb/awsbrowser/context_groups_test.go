package awsbrowser

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadAndApplyContextGroups(t *testing.T) {
	path := filepath.Join(t.TempDir(), AWSContextGroupsFilename)
	document := `{
  "version": 1,
  "groups": [{
    "name": "UDG",
    "profiles": ["lg-udg-ops", "lg-udg-adm"],
    "regions": ["ap-northeast-2", "ap-southeast-1", "us-east-1", "eu-central-1"],
    "default_profile": "lg-udg-ops",
    "default_region": "ap-northeast-2"
  }]
}`
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	groups, err := LoadContextGroups(path)
	if err != nil || len(groups) != 1 {
		t.Fatalf("groups=%+v error=%v", groups, err)
	}
	choices := ApplyContextGroups([]ContextChoice{
		{Profile: "personal", Region: "us-west-2"},
		{Profile: "lg-udg-adm", Region: "eu-west-1"},
		{Profile: "lg-udg-ops"},
	}, groups)
	if len(choices) != 3 || choices[0].Profile != "lg-udg-ops" || choices[0].Group != "UDG" || choices[0].Region != "ap-northeast-2" ||
		!reflect.DeepEqual(choices[0].Regions, groups[0].Regions) || choices[1].Profile != "lg-udg-adm" || choices[2].Profile != "personal" {
		t.Fatalf("choices=%+v", choices)
	}
	regions, err := ParseRegionSet("ap-northeast-2,ap-southeast-1,us-east-1,eu-central-1", "ap-northeast-2")
	if err != nil || !reflect.DeepEqual(regions, groups[0].Regions) {
		t.Fatalf("regions=%+v error=%v", regions, err)
	}
}

func TestContextGroupsMissingAndStrictValidation(t *testing.T) {
	missing := filepath.Join(t.TempDir(), AWSContextGroupsFilename)
	if groups, err := LoadContextGroups(missing); err != nil || groups != nil {
		t.Fatalf("missing groups=%+v error=%v", groups, err)
	}
	for name, document := range map[string]string{
		"unknown field":     `{"version":1,"groups":[],"credentials":"no"}`,
		"duplicate profile": `{"version":1,"groups":[{"name":"one","profiles":["dev"],"regions":["us-east-1"]},{"name":"two","profiles":["dev"],"regions":["us-west-2"]}]}`,
		"bad default":       `{"version":1,"groups":[{"name":"one","profiles":["dev"],"regions":["us-east-1"],"default_region":"moon-1"}]}`,
		"trailing JSON":     `{"version":1,"groups":[]} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), AWSContextGroupsFilename)
			if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadContextGroups(path); !errors.Is(err, ErrInvalidContextGroups) {
				t.Fatalf("error=%v", err)
			}
		})
	}
	oversized := filepath.Join(t.TempDir(), AWSContextGroupsFilename)
	if err := os.WriteFile(oversized, make([]byte, maxContextGroupsFileSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadContextGroups(oversized); !errors.Is(err, ErrInvalidContextGroups) {
		t.Fatalf("oversized error=%v", err)
	}
}
