// Package importer reads live state and prints the config block that describes
// it.
//
// Writing one of these by hand is how a config ends up describing an app that
// does not exist — a service id off by a character, a bucket that was renamed,
// a production branch nobody changed in the file. Reading it off the account is
// the difference between a tool you configure and a tool you point.
//
// One rule: a live secret is never copied into the output. A variable that
// looks like a credential comes back as a valueEnv reference with no value, so
// importing an app cannot be the thing that writes its secrets to disk.
package importer

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/zoolcoder/upkeep/internal/config"
)

// Render is the read half of the Render API.
type Render interface {
	EnvVars(ctx context.Context, serviceID string) (map[string]string, error)
}

// CF is the read half of the Cloudflare API.
type CF interface {
	Do(ctx context.Context, method, path string, body, result any) error
}

type Request struct {
	Name      string
	AccountID string
	ServiceID string
	Bucket    string
	Pages     string
}

// secretish is config's rule, not a second opinion on it. Two lists that can
// drift is how an importer ends up writing a file its own validator refuses.
func secretish(key string) bool { return config.LooksSecret(key) }

// Import renders a YAML app block from whatever surfaces the request names.
func Import(ctx context.Context, req Request, render Render, cf CF) (string, error) {
	if req.Name == "" {
		return "", fmt.Errorf("an imported app needs a name")
	}

	var b strings.Builder
	fmt.Fprintf(&b, "  - name: %s\n", req.Name)

	if req.ServiceID != "" {
		if render == nil {
			return "", fmt.Errorf("importing a Render service needs RENDER_API_KEY")
		}
		block, err := renderBlock(ctx, req.ServiceID, render)
		if err != nil {
			return "", err
		}
		b.WriteString(block)
	}

	if req.Bucket != "" {
		if cf == nil {
			return "", fmt.Errorf("importing an R2 bucket needs CLOUDFLARE_API_TOKEN")
		}
		block, err := r2Block(ctx, req, cf)
		if err != nil {
			return "", err
		}
		b.WriteString(block)
	}

	if req.Pages != "" {
		if cf == nil {
			return "", fmt.Errorf("importing a Pages project needs CLOUDFLARE_API_TOKEN")
		}
		block, err := pagesBlock(ctx, req, cf)
		if err != nil {
			return "", err
		}
		b.WriteString(block)
	}
	return b.String(), nil
}

func renderBlock(ctx context.Context, serviceID string, api Render) (string, error) {
	env, err := api.EnvVars(ctx, serviceID)
	if err != nil {
		return "", fmt.Errorf("read %s environment: %w", serviceID, err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "    render:\n      serviceId: %s\n", serviceID)
	if len(env) == 0 {
		return b.String(), nil
	}
	fmt.Fprintf(&b, "      env:\n")

	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var empty []string
	for _, k := range keys {
		// A variable set to the empty string cannot be written here: the config
		// reads an empty value as "no source given", which is the check that
		// catches a forgotten valueEnv. Leaving it out is the honest outcome —
		// upkeep manages what the config names and never deletes by
		// omission, so an unnamed variable stays exactly as it is.
		if env[k] == "" && !secretish(k) {
			empty = append(empty, k)
			continue
		}
		fmt.Fprintf(&b, "        - key: %s\n", k)
		if secretish(k) {
			// The live value is deliberately dropped on the floor.
			fmt.Fprintf(&b, "          valueEnv: %s\n", k)
			continue
		}
		fmt.Fprintf(&b, "          value: %s\n", quote(env[k]))
	}
	if len(empty) > 0 {
		fmt.Fprintf(&b, "      # set but empty on the service, so left unmanaged: %s\n",
			strings.Join(empty, ", "))
	}
	return b.String(), nil
}

type managedDomain struct {
	Enabled bool   `json:"enabled"`
	Domain  string `json:"domain"`
}

type corsDoc struct {
	Rules []struct {
		Allowed struct {
			Origins []string `json:"origins"`
			Methods []string `json:"methods"`
			Headers []string `json:"headers"`
		} `json:"allowed"`
		ExposeHeaders []string `json:"exposeHeaders"`
		MaxAgeSeconds int      `json:"maxAgeSeconds"`
	} `json:"rules"`
}

func r2Block(ctx context.Context, req Request, cf CF) (string, error) {
	if req.AccountID == "" {
		return "", fmt.Errorf("importing R2 needs an account id")
	}
	base := fmt.Sprintf("/accounts/%s/r2/buckets/%s", req.AccountID, req.Bucket)

	var b strings.Builder
	fmt.Fprintf(&b, "    r2:\n      accountId: %s\n      bucket: %s\n", req.AccountID, req.Bucket)

	var managed managedDomain
	if err := cf.Do(ctx, "GET", base+"/domains/managed", nil, &managed); err == nil && managed.Enabled {
		fmt.Fprintf(&b, "      public: true\n")
	}

	var cors corsDoc
	if err := cf.Do(ctx, "GET", base+"/cors", nil, &cors); err == nil && len(cors.Rules) > 0 {
		fmt.Fprintf(&b, "      cors:\n")
		for _, r := range cors.Rules {
			fmt.Fprintf(&b, "        - origins:\n")
			for _, o := range r.Allowed.Origins {
				fmt.Fprintf(&b, "            - %s\n", o)
			}
			fmt.Fprintf(&b, "          methods: [%s]\n", strings.Join(r.Allowed.Methods, ", "))
			if len(r.Allowed.Headers) > 0 {
				fmt.Fprintf(&b, "          headers: [%s]\n", strings.Join(r.Allowed.Headers, ", "))
			}
			if len(r.ExposeHeaders) > 0 {
				fmt.Fprintf(&b, "          exposeHeaders: [%s]\n", strings.Join(r.ExposeHeaders, ", "))
			}
			if r.MaxAgeSeconds > 0 {
				fmt.Fprintf(&b, "          maxAgeSeconds: %d\n", r.MaxAgeSeconds)
			}
		}
	}
	return b.String(), nil
}

type pagesProject struct {
	Name             string   `json:"name"`
	ProductionBranch string   `json:"production_branch"`
	Domains          []string `json:"domains"`
}

func pagesBlock(ctx context.Context, req Request, cf CF) (string, error) {
	if req.AccountID == "" {
		return "", fmt.Errorf("importing Pages needs an account id")
	}
	var live pagesProject
	if err := cf.Do(ctx, "GET",
		fmt.Sprintf("/accounts/%s/pages/projects/%s", req.AccountID, req.Pages), nil, &live); err != nil {
		return "", fmt.Errorf("read pages project %s: %w", req.Pages, err)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "    pages:\n      accountId: %s\n      project: %s\n", req.AccountID, req.Pages)
	if live.ProductionBranch != "" {
		fmt.Fprintf(&b, "      productionBranch: %s\n", live.ProductionBranch)
	}
	if len(live.Domains) > 0 {
		fmt.Fprintf(&b, "      domains:\n")
		for _, d := range live.Domains {
			fmt.Fprintf(&b, "        - %s\n", d)
		}
	}
	return b.String(), nil
}

// quote wraps a value YAML would otherwise read as something else.
//
// Every config value is a string, so anything YAML would decode as another type
// has to be quoted or the import will not parse back in. VISIT_FEE_SDG=5000 is
// the case that found this: unquoted it decodes as an int, and loading the
// config upkeep just wrote fails.
func quote(v string) string {
	if v == "" {
		return `""`
	}
	if strings.ContainsAny(v, `:#{}[],&*?|<>=!%@\"'`+"\n\t") ||
		strings.HasPrefix(v, " ") || strings.HasSuffix(v, " ") {
		return `"` + strings.ReplaceAll(strings.ReplaceAll(v, `\`, `\\`), `"`, `\"`) + `"`
	}
	switch strings.ToLower(v) {
	case "true", "false", "yes", "no", "on", "off", "null", "~":
		return `"` + v + `"`
	}
	// A number, in any form YAML recognises.
	if _, err := strconv.ParseFloat(v, 64); err == nil {
		return `"` + v + `"`
	}
	if _, err := strconv.ParseInt(v, 0, 64); err == nil {
		return `"` + v + `"`
	}
	return v
}
