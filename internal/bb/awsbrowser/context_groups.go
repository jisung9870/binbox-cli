package awsbrowser

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

const (
	AWSContextGroupsFilename = "aws-contexts.json"
	maxContextGroupsFileSize = 64 << 10
	maxContextGroups         = 32
	maxGroupProfiles         = 64
	maxGroupRegions          = 16
)

var ErrInvalidContextGroups = errors.New("invalid AWS context groups")

type ContextGroup struct {
	Name           string   `json:"name"`
	Profiles       []string `json:"profiles"`
	Regions        []string `json:"regions"`
	DefaultProfile string   `json:"default_profile,omitempty"`
	DefaultRegion  string   `json:"default_region,omitempty"`
}

type contextGroupsFile struct {
	Version int            `json:"version"`
	Groups  []ContextGroup `json:"groups"`
}

// LoadContextGroups reads non-secret AWS navigation metadata. A missing file
// preserves the single-region browser behavior.
func LoadContextGroups(path string) ([]ContextGroup, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, ErrInvalidContextGroups
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() > maxContextGroupsFileSize {
		return nil, ErrInvalidContextGroups
	}

	decoder := json.NewDecoder(io.LimitReader(file, maxContextGroupsFileSize+1))
	decoder.DisallowUnknownFields()
	var document contextGroupsFile
	if err := decoder.Decode(&document); err != nil {
		return nil, ErrInvalidContextGroups
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, ErrInvalidContextGroups
	}
	if document.Version != 1 || len(document.Groups) > maxContextGroups {
		return nil, ErrInvalidContextGroups
	}

	groups := make([]ContextGroup, len(document.Groups))
	seenNames := make(map[string]bool, len(document.Groups))
	seenProfiles := make(map[string]bool)
	for index, group := range document.Groups {
		normalized, err := normalizeContextGroup(group)
		if err != nil || seenNames[strings.ToLower(normalized.Name)] {
			return nil, ErrInvalidContextGroups
		}
		for _, profile := range normalized.Profiles {
			if seenProfiles[profile] {
				return nil, ErrInvalidContextGroups
			}
			seenProfiles[profile] = true
		}
		seenNames[strings.ToLower(normalized.Name)] = true
		groups[index] = normalized
	}
	return groups, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); errors.Is(err, io.EOF) {
		return nil
	}
	return ErrInvalidContextGroups
}

func normalizeContextGroup(group ContextGroup) (ContextGroup, error) {
	group.Name = strings.TrimSpace(group.Name)
	if group.Name == "" || len(group.Name) > 64 || containsContextControl(group.Name) || len(group.Profiles) == 0 || len(group.Profiles) > maxGroupProfiles || len(group.Regions) == 0 || len(group.Regions) > maxGroupRegions {
		return ContextGroup{}, ErrInvalidContextGroups
	}
	profiles, err := canonicalContextValues(group.Profiles, func(value string) bool {
		return ValidateContextSelection(value, "") == nil && value != ""
	})
	if err != nil {
		return ContextGroup{}, err
	}
	regions, err := canonicalContextValues(group.Regions, func(value string) bool {
		return ValidateContextSelection("", value) == nil && value != ""
	})
	if err != nil {
		return ContextGroup{}, err
	}
	if group.DefaultProfile == "" {
		group.DefaultProfile = profiles[0]
	}
	if group.DefaultRegion == "" {
		group.DefaultRegion = regions[0]
	}
	if !containsString(profiles, group.DefaultProfile) || !containsString(regions, group.DefaultRegion) {
		return ContextGroup{}, ErrInvalidContextGroups
	}
	group.Profiles = currentFirst(profiles, group.DefaultProfile)
	group.Regions = currentFirst(regions, group.DefaultRegion)
	return group, nil
}

func containsContextControl(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

func canonicalContextValues(values []string, valid func(string) bool) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if !valid(value) || seen[value] {
			return nil, ErrInvalidContextGroups
		}
		seen[value] = true
		result = append(result, value)
	}
	return result, nil
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func currentFirst(values []string, current string) []string {
	result := make([]string, 0, len(values))
	if current != "" && containsString(values, current) {
		result = append(result, current)
	}
	for _, value := range values {
		if value != current {
			result = append(result, value)
		}
	}
	return result
}

// ApplyContextGroups annotates locally discovered profiles and orders group
// defaults first without introducing profiles that AWS itself did not list.
func ApplyContextGroups(choices []ContextChoice, groups []ContextGroup) []ContextChoice {
	byProfile := make(map[string]ContextGroup)
	groupOrder := make(map[string]int)
	for index, group := range groups {
		groupOrder[group.Name] = index
		for _, profile := range group.Profiles {
			byProfile[profile] = group
		}
	}
	result := append([]ContextChoice(nil), choices...)
	for index := range result {
		group, ok := byProfile[result[index].Profile]
		if !ok {
			continue
		}
		result[index].Group = group.Name
		result[index].Regions = append([]string(nil), group.Regions...)
		if result[index].Profile == group.DefaultProfile || result[index].Region == "" {
			result[index].Region = group.DefaultRegion
		}
		result[index].Regions = currentFirst(result[index].Regions, result[index].Region)
	}
	sort.SliceStable(result, func(left, right int) bool {
		leftGroup, leftOK := groupOrder[result[left].Group]
		rightGroup, rightOK := groupOrder[result[right].Group]
		if leftOK != rightOK {
			return leftOK
		}
		if leftOK && leftGroup != rightGroup {
			return leftGroup < rightGroup
		}
		leftConfig, leftGrouped := byProfile[result[left].Profile]
		rightConfig, rightGrouped := byProfile[result[right].Profile]
		if leftGrouped && rightGrouped && leftConfig.Name == rightConfig.Name {
			if result[left].Profile == leftConfig.DefaultProfile {
				return true
			}
			if result[right].Profile == rightConfig.DefaultProfile {
				return false
			}
		}
		return false
	})
	return result
}

// CanonicalRegionSet validates, deduplicates, and places the current region
// first. The returned comma-separated value is safe to keep on an Intent.
func CanonicalRegionSet(regions []string, current string) (string, error) {
	if current == "" || ValidateContextSelection("", current) != nil || len(regions) == 0 || len(regions) > maxGroupRegions {
		return "", ErrInvalidContextGroups
	}
	seen := make(map[string]bool, len(regions))
	validated := make([]string, 0, len(regions)+1)
	for _, region := range append([]string{current}, regions...) {
		if ValidateContextSelection("", region) != nil || region == "" {
			return "", ErrInvalidContextGroups
		}
		if !seen[region] {
			seen[region] = true
			validated = append(validated, region)
		}
	}
	return strings.Join(validated, ","), nil
}

func ParseRegionSet(value, current string) ([]string, error) {
	if value == "" {
		return nil, nil
	}
	regions := strings.Split(value, ",")
	canonical, err := CanonicalRegionSet(regions, current)
	if err != nil || canonical != value {
		return nil, fmt.Errorf("%w: non-canonical region set", ErrInvalidContextGroups)
	}
	return strings.Split(canonical, ","), nil
}
