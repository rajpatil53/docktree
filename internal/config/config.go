// Package config loads docktree.toml.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/rajpatil53/docktree/internal/identity"
)

type Warning struct {
	Key     string
	Message string
}

type Service struct {
	HostPortEnv  string
	Fixed        bool
	Autostart    bool
	Expose       string
	ProxyPort    int
	PublicURLEnv string
}

type Proxy struct {
	Enabled   bool
	Engine    string
	DNSSuffix string
	HTTPPort  int
	HTTPSPort int
}

type Stateful struct {
	DefaultStrategy string
	Engine          string
	SnapshotSource  string
	SourceDB        string
	Superuser       string
	Env             map[string]string
}

type Secrets struct {
	Wrapper string `toml:"wrapper"`
}

// DB is retained for the legacy command files that are retired later in the
// redesign. New code should read Stateful instead.
type DB struct {
	Engine       string `toml:"engine"`
	InfraService string `toml:"infra_service"`
	NameTemplate string `toml:"name_template"`
	TestSuffix   string `toml:"test_suffix"`
}

type Config struct {
	App              string
	Compose          string
	Shared           []string
	SharedServices   []string
	Services         map[string]Service
	Stateful         map[string]Stateful
	Env              map[string]string
	Secrets          Secrets
	Proxy            Proxy
	DB               DB
	MainBranch       string
	Ports            string
	ImageTagTemplate string
	Warnings         []Warning
}

type TemplateContext struct {
	App      string
	Slug     string
	MainSlug string
}

type rawConfig struct {
	App              string                 `toml:"app"`
	Compose          string                 `toml:"compose"`
	Shared           []string               `toml:"shared"`
	SharedServices   []string               `toml:"shared_services"`
	Services         map[string]rawService  `toml:"services"`
	Stateful         map[string]rawStateful `toml:"stateful"`
	Env              map[string]string      `toml:"env"`
	Secrets          Secrets                `toml:"secrets"`
	Proxy            rawProxy               `toml:"proxy"`
	DB               DB                     `toml:"db"`
	MainBranch       string                 `toml:"main_branch"`
	Ports            string                 `toml:"ports"`
	ImageTagTemplate string                 `toml:"image_tag_template"`
}

type rawService struct {
	HostPortEnv  string `toml:"host_port_env"`
	Fixed        bool   `toml:"fixed"`
	Autostart    *bool  `toml:"autostart"`
	Expose       string `toml:"expose"`
	ProxyPort    int    `toml:"proxy_port"`
	PublicURLEnv string `toml:"public_url_env"`
}

type rawProxy struct {
	Enabled   bool   `toml:"enabled"`
	Engine    string `toml:"engine"`
	DNSSuffix string `toml:"dns_suffix"`
	HTTPPort  int    `toml:"http_port"`
	HTTPSPort int    `toml:"https_port"`
}

type rawStateful struct {
	DefaultStrategy string            `toml:"default_strategy"`
	Engine          string            `toml:"engine"`
	SnapshotSource  string            `toml:"snapshot_source"`
	SourceDB        string            `toml:"source_db"`
	Superuser       string            `toml:"superuser"`
	Env             map[string]string `toml:"env"`
}

func LoadFile(path string) (*Config, error) {
	cfg := defaultConfig(path)

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}

	var raw rawConfig
	meta, err := toml.Decode(string(data), &raw)
	if err != nil {
		return nil, err
	}

	if raw.App != "" {
		cfg.App = identity.AppName(raw.App, filepath.Dir(path))
	}
	cfg.Compose = raw.Compose
	cfg.Shared = append([]string(nil), raw.Shared...)
	if len(cfg.Shared) == 0 && len(raw.SharedServices) > 0 {
		cfg.Shared = append([]string(nil), raw.SharedServices...)
	}
	cfg.SharedServices = append([]string(nil), cfg.Shared...)
	cfg.Services = normalizeServices(raw.Services)
	cfg.Stateful = normalizeStateful(raw.Stateful)
	cfg.Env = normalizeEnv(raw.Env)
	cfg.Secrets = raw.Secrets
	cfg.Proxy = normalizeProxy(raw.Proxy)
	cfg.DB = normalizeDB(raw.DB, cfg.Stateful)
	cfg.MainBranch = raw.MainBranch
	if cfg.MainBranch == "" {
		cfg.MainBranch = "main"
	}
	cfg.Ports = raw.Ports
	if cfg.Ports == "" {
		cfg.Ports = "hashed"
	}
	cfg.ImageTagTemplate = raw.ImageTagTemplate
	cfg.Warnings = warnings(meta.Undecoded())
	if err := validateEnvEntries("env", cfg.Env); err != nil {
		return nil, err
	}
	for name, st := range cfg.Stateful {
		if err := validateEnvEntries("stateful."+name+".env", st.Env); err != nil {
			return nil, err
		}
	}
	return cfg, nil
}

// validateEnvEntries rejects entries the env artifact cannot carry: the
// artifact is a line-based KEY=VALUE file, so newlines and '='-bearing keys
// would round-trip as truncated values or bogus extra keys — which the
// process-env override channel would then elevate over wrapper secrets.
func validateEnvEntries(section string, env map[string]string) error {
	for key, value := range env {
		if key == "" || strings.ContainsAny(key, "=\n\r\t ") {
			return fmt.Errorf("[%s] key %q must be a single token without whitespace or '='", section, key)
		}
		if strings.ContainsAny(value, "\n\r") {
			return fmt.Errorf("[%s] value for %s must be single-line", section, key)
		}
	}
	return nil
}

func (c Config) RenderTemplates(ctx TemplateContext) Config {
	if ctx.App == "" {
		ctx.App = c.App
	}
	out := c
	out.Shared = append([]string(nil), c.Shared...)
	out.SharedServices = append([]string(nil), c.SharedServices...)
	out.Services = make(map[string]Service, len(c.Services))
	for name, svc := range c.Services {
		out.Services[name] = svc
	}
	out.Stateful = make(map[string]Stateful, len(c.Stateful))
	for name, st := range c.Stateful {
		st.DefaultStrategy = Expand(st.DefaultStrategy, ctx)
		st.Engine = Expand(st.Engine, ctx)
		st.SnapshotSource = Expand(st.SnapshotSource, ctx)
		st.SourceDB = Expand(st.SourceDB, ctx)
		st.Env = expandMap(st.Env, ctx)
		out.Stateful[name] = st
	}
	out.Env = make(map[string]string, len(c.Env))
	for key, value := range c.Env {
		out.Env[key] = Expand(value, ctx)
	}
	out.Warnings = append([]Warning(nil), c.Warnings...)
	out.DB.NameTemplate = Expand(out.DB.NameTemplate, ctx)
	return out
}

func Expand(value string, ctx TemplateContext) string {
	value = strings.ReplaceAll(value, "{app}", ctx.App)
	value = strings.ReplaceAll(value, "{slug}", ctx.Slug)
	value = strings.ReplaceAll(value, "{main_slug}", ctx.MainSlug)
	return value
}

func expandMap(values map[string]string, ctx TemplateContext) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = Expand(value, ctx)
	}
	return out
}

func defaultConfig(path string) *Config {
	return &Config{
		App:            identity.AppName("", filepath.Dir(path)),
		Services:       map[string]Service{},
		Stateful:       map[string]Stateful{},
		Env:            map[string]string{},
		DB:             DB{InfraService: "postgres", TestSuffix: "_test"},
		MainBranch:     "main",
		Ports:          "hashed",
		SharedServices: []string{},
		Proxy:          normalizeProxy(rawProxy{}),
	}
}

func normalizeServices(raw map[string]rawService) map[string]Service {
	services := make(map[string]Service, len(raw))
	for name, svc := range raw {
		autostart := true
		if svc.Autostart != nil {
			autostart = *svc.Autostart
		}
		services[name] = Service{
			HostPortEnv:  svc.HostPortEnv,
			Fixed:        svc.Fixed,
			Autostart:    autostart,
			Expose:       svc.Expose,
			ProxyPort:    svc.ProxyPort,
			PublicURLEnv: svc.PublicURLEnv,
		}
	}
	return services
}

func normalizeProxy(raw rawProxy) Proxy {
	p := Proxy{
		Enabled:   raw.Enabled,
		Engine:    raw.Engine,
		DNSSuffix: raw.DNSSuffix,
		HTTPPort:  raw.HTTPPort,
		HTTPSPort: raw.HTTPSPort,
	}
	if p.Engine == "" {
		p.Engine = "caddy"
	}
	if p.DNSSuffix == "" {
		p.DNSSuffix = "localhost"
	}
	if p.HTTPPort == 0 {
		p.HTTPPort = 80
	}
	if p.HTTPSPort == 0 {
		p.HTTPSPort = 443
	}
	return p
}

func normalizeStateful(raw map[string]rawStateful) map[string]Stateful {
	stateful := make(map[string]Stateful, len(raw))
	for name, st := range raw {
		strategy := st.DefaultStrategy
		if strategy == "" {
			strategy = "shared"
		}
		superuser := st.Superuser
		if superuser == "" {
			// The official Postgres image's initdb creates this superuser role
			// (and the matching maintenance DB) by default; apps whose
			// POSTGRES_USER differs override it here.
			superuser = "postgres"
		}
		stateful[name] = Stateful{
			DefaultStrategy: strategy,
			Engine:          st.Engine,
			SnapshotSource:  st.SnapshotSource,
			SourceDB:        st.SourceDB,
			Superuser:       superuser,
			Env:             normalizeEnv(st.Env),
		}
	}
	return stateful
}

func normalizeEnv(raw map[string]string) map[string]string {
	env := make(map[string]string, len(raw))
	for key, value := range raw {
		env[key] = value
	}
	return env
}

func normalizeDB(db DB, stateful map[string]Stateful) DB {
	if db.InfraService == "" {
		db.InfraService = "postgres"
	}
	if db.TestSuffix == "" {
		db.TestSuffix = "_test"
	}
	if pg, ok := stateful["postgres"]; ok && db.Engine == "" {
		db.Engine = pg.Engine
	}
	return db
}

func warnings(keys []toml.Key) []Warning {
	warnings := make([]Warning, 0, len(keys))
	for _, key := range keys {
		k := key.String()
		warnings = append(warnings, Warning{
			Key:     k,
			Message: "unknown docktree.toml key: " + k,
		})
	}
	return warnings
}
