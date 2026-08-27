package awsbrowser

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

const maxSharedProfileFileSize = 1 << 20

var (
	errSharedProfileRead     = errors.New("AWS profile configuration could not be read")
	errSharedProfileTooLarge = errors.New("AWS profile configuration is too large")
)

// validateNamedProfileSource loads only the two AWS shared-profile documents
// selected by the supplied environment snapshot. The document bytes remain
// local to this call and are cleared before it returns.
func validateNamedProfileSource(ctx context.Context, profile string, env []string) error {
	configPath, credentialsPath := sharedProfilePaths(env, runtime.GOOS)
	configData, err := readOptionalSharedProfileFile(ctx, configPath)
	if err != nil {
		return err
	}
	defer clear(configData)

	credentialsData, err := readOptionalSharedProfileFile(ctx, credentialsPath)
	if err != nil {
		return err
	}
	defer clear(credentialsData)

	if err := ctx.Err(); err != nil {
		return err
	}
	_, err = ClassifyProfileSource(profile, configData, credentialsData)
	return err
}

func sharedProfilePaths(env []string, goos string) (configPath, credentialsPath string) {
	home := sharedProfileHome(env, goos)
	if home != "" {
		configPath = filepath.Join(home, ".aws", "config")
		credentialsPath = filepath.Join(home, ".aws", "credentials")
	}
	if value, ok := profileEnvValue(env, "AWS_CONFIG_FILE", goos); ok && value != "" {
		configPath = value
	}
	if value, ok := profileEnvValue(env, "AWS_SHARED_CREDENTIALS_FILE", goos); ok && value != "" {
		credentialsPath = value
	}
	return configPath, credentialsPath
}

func sharedProfileHome(env []string, goos string) string {
	switch goos {
	case "windows":
		if home, ok := profileEnvValue(env, "USERPROFILE", goos); ok && home != "" {
			return home
		}
		drive, _ := profileEnvValue(env, "HOMEDRIVE", goos)
		path, _ := profileEnvValue(env, "HOMEPATH", goos)
		if drive != "" || path != "" {
			return drive + path
		}
	case "plan9":
		if home, ok := profileEnvValue(env, "home", goos); ok {
			return home
		}
	default:
		if home, ok := profileEnvValue(env, "HOME", goos); ok {
			return home
		}
	}
	return ""
}

func profileEnvValue(env []string, key, goos string) (string, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		name, value, found := strings.Cut(env[i], "=")
		if !found {
			continue
		}
		if name == key || goos == "windows" && strings.EqualFold(name, key) {
			return value, true
		}
	}
	return "", false
}

func readOptionalSharedProfileFile(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if path == "" {
		return nil, nil
	}
	file, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errSharedProfileRead
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errSharedProfileRead
	}
	if info.Size() > maxSharedProfileFileSize {
		return nil, errSharedProfileTooLarge
	}

	data, err := io.ReadAll(io.LimitReader(contextFileReader{ctx: ctx, reader: file}, maxSharedProfileFileSize+1))
	if err != nil {
		clear(data)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, errSharedProfileRead
	}
	if len(data) > maxSharedProfileFileSize {
		clear(data)
		return nil, errSharedProfileTooLarge
	}
	if err := ctx.Err(); err != nil {
		clear(data)
		return nil, err
	}
	return data, nil
}

type contextFileReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextFileReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
