package config

import (
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// varRef matches ${NAME} anywhere in a string value.
var varRef = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Load reads a config file, expands ${VAR} references, and validates it.
//
// A path of "-" reads standard input, so `import` and `plan` compose:
//
//	upkeep import -name app -render srv-… | upkeep plan -config -
//
// getenv is injected so tests do not touch the process environment. Nil means
// os.Getenv.
func Load(path string, getenv func(string) string) (Config, error) {
	return LoadFrom(path, os.Stdin, getenv)
}

// LoadFrom is Load with the standard input it should read for "-".
func LoadFrom(path string, stdin io.Reader, getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	var raw []byte
	var err error
	if path == "-" {
		raw, err = io.ReadAll(stdin)
	} else {
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}
	return Parse(raw, getenv)
}

// Parse is Load without the filesystem.
func Parse(raw []byte, getenv func(string) string) (Config, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	expanded, err := expand(string(raw), getenv)
	if err != nil {
		return Config{}, err
	}

	var cfg Config
	dec := yaml.NewDecoder(strings.NewReader(expanded))
	// A typo in a key is a silently ignored setting otherwise, which on this
	// tool means a variable nobody notices is unset.
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}
	if err := Validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// expand replaces ${VAR} with the environment's value. An unset variable is an
// error rather than an empty string: an empty accountId reaches the API as a
// malformed path and comes back as a confusing 404.
func expand(s string, getenv func(string) string) (string, error) {
	var missing []string
	out := varRef.ReplaceAllStringFunc(s, func(match string) string {
		name := varRef.FindStringSubmatch(match)[1]
		value := getenv(name)
		if value == "" {
			missing = append(missing, name)
			return match
		}
		return value
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("config references unset variables: %s", strings.Join(dedupe(missing), ", "))
	}
	return out, nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
