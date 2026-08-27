package integration

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jisung9870/binbox-cli/internal/bb/awsbrowser"
)

const maxConfigBytes = 1 << 20

var errRegionRequired = errors.New("AWS region is required")

type contextResolver struct {
	env []string
}

func newContextResolver(env []string) contextResolver {
	return contextResolver{env: append([]string(nil), env...)}
}

func validExplicitContextRequest(profile, region string) bool {
	return awsbrowser.ValidateContextSelection(profile, region) == nil
}

func (resolver contextResolver) Resolve(ctx context.Context, profile, region string) (awsbrowser.ContextSpec, error) {
	if err := ctx.Err(); err != nil {
		return awsbrowser.ContextSpec{}, err
	}
	profile = strings.TrimSpace(profile)
	region = strings.TrimSpace(region)
	mode := awsbrowser.ContextModeAmbient
	if profile != "" {
		mode = awsbrowser.ContextModeNamedProfile
	}
	if region == "" {
		region = resolver.envValue("AWS_REGION")
	}
	if region == "" {
		region = resolver.envValue("AWS_DEFAULT_REGION")
	}
	if region == "" {
		selected := profile
		if selected == "" {
			selected = resolver.envValue("AWS_PROFILE")
		}
		if selected == "" {
			selected = resolver.envValue("AWS_DEFAULT_PROFILE")
		}
		if selected == "" {
			selected = "default"
		}
		var err error
		region, err = resolver.sharedConfigRegion(ctx, selected)
		if err != nil {
			return awsbrowser.ContextSpec{}, err
		}
	}
	if region == "" {
		return awsbrowser.ContextSpec{}, errRegionRequired
	}
	return awsbrowser.ContextSpec{Mode: mode, Profile: profile, Region: region}, nil
}

func (resolver contextResolver) envValue(name string) string {
	for index := len(resolver.env) - 1; index >= 0; index-- {
		key, value, ok := strings.Cut(resolver.env[index], "=")
		if ok && key == name {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (resolver contextResolver) sharedConfigRegion(ctx context.Context, profile string) (string, error) {
	path := resolver.envValue("AWS_CONFIG_FILE")
	if path == "" {
		home := resolver.envValue("HOME")
		if home == "" {
			return "", nil
		}
		path = filepath.Join(home, ".aws", "config")
	}
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", errRegionRequired
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxConfigBytes {
		return "", errRegionRequired
	}

	reader := bufio.NewReader(io.LimitReader(file, maxConfigBytes+1))
	wanted := profile
	if profile != "default" {
		wanted = "profile " + profile
	}
	active := false
	region := ""
	currentKey := ""
	currentValueEmpty := false
	consumed := 0
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		line, readErr := reader.ReadString('\n')
		consumed += len(line)
		if consumed > maxConfigBytes {
			return "", errRegionRequired
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		trimmed := strings.TrimSpace(line)
		if header, ok := sharedConfigHeader(line); ok {
			active = header == wanted
			currentKey = ""
			currentValueEmpty = false
		} else if active && trimmed != "" && !strings.HasPrefix(trimmed, "#") && !strings.HasPrefix(trimmed, ";") {
			key, value, ok := splitSharedConfigProperty(line)
			if ok {
				indented := len(line) > 0 && (line[0] == ' ' || line[0] == '\t')
				if !(indented && currentKey != "" && currentValueEmpty) {
					key = strings.ToLower(strings.TrimSpace(key))
					value = strings.TrimSpace(trimSharedConfigPropertyComment(value))
					value = unquoteSharedConfigValue(value)
					currentKey = key
					currentValueEmpty = value == ""
					if key == "region" {
						region = value
					}
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			return region, nil
		}
		if readErr != nil {
			return "", errRegionRequired
		}
	}
}

func sharedConfigHeader(line string) (string, bool) {
	header, _, _ := strings.Cut(line, "#")
	header, _, _ = strings.Cut(header, ";")
	header = strings.TrimSpace(header)
	if len(header) < 2 || header[0] != '[' || header[len(header)-1] != ']' {
		return "", false
	}
	return strings.TrimSpace(header[1 : len(header)-1]), true
}

func splitSharedConfigProperty(line string) (key, value string, ok bool) {
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
	if key == "" {
		return "", "", false
	}
	return key, line[separator+1:], true
}

func trimSharedConfigPropertyComment(value string) string {
	for _, marker := range []string{" #", " ;", "\t#", "\t;"} {
		if before, _, found := strings.Cut(value, marker); found {
			value = before
		}
	}
	return value
}

func unquoteSharedConfigValue(value string) string {
	if len(value) >= 2 && ((value[0] == '\'' && value[len(value)-1] == '\'') ||
		(value[0] == '"' && value[len(value)-1] == '"')) {
		return value[1 : len(value)-1]
	}
	return value
}
