package cmd

import (
	"strings"
	"testing"

	"github.com/rajpatil53/docktree/internal/compose"
	"github.com/rajpatil53/docktree/internal/config"
	"github.com/rajpatil53/docktree/internal/identity"
)

func TestEnvArtifactWritesOnlyIsolatedStatefulEnvAndOverridesTopLevelEnv(t *testing.T) {
	r := &resolved{
		manifest: &config.Config{
			App: "shop",
			Env: map[string]string{
				"POSTGRES_PRIMARY_DB": "shared_default",
			},
			Stateful: map[string]config.Stateful{
				"postgres": {
					DefaultStrategy: "shared",
					Engine:          "postgres",
					SourceDB:        "shop_shared",
					Env: map[string]string{
						"POSTGRES_PRIMARY_DB": "shop_shared_{slug}",
					},
				},
				"redis": {
					DefaultStrategy: "shared",
					Engine:          "redis",
					Env: map[string]string{
						"REDIS_NAMESPACE": "redis_{slug}",
					},
				},
			},
		},
		slug:          "feature_one",
		mainSlug:      "main",
		names:         identity.Derive("shop", "feature_one"),
		statefulModes: map[string]string{"postgres": "isolated", "redis": "shared"},
	}

	artifact, err := envArtifact(r, nil)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Env["POSTGRES_PRIMARY_DB"] != "shop_shared_feature_one" {
		t.Fatalf("postgres env = %#v", artifact.Env)
	}
	if _, ok := artifact.Env["REDIS_NAMESPACE"]; ok {
		t.Fatalf("shared redis stateful env should be omitted: %#v", artifact.Env)
	}
}

func TestEnvArtifactInjectsPublicURLForProxiedSharedService(t *testing.T) {
	model, err := compose.ParseConfigJSON([]byte(`{
		"services": {"minio": {"image": "minio/minio", "ports": [{"target": 9000, "published": "${MINIO_PORT:-9000}"}]}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	r := &resolved{
		manifest: &config.Config{
			App:    "shop",
			Shared: []string{"minio"},
			Services: map[string]config.Service{
				"minio": {Expose: "http", ProxyPort: 9000, PublicURLEnv: "MINIO_PUBLIC_URL"},
			},
			Stateful: map[string]config.Stateful{},
			Env:      map[string]string{},
			Proxy:    config.Proxy{Enabled: true, DNSSuffix: "localhost", HTTPSPort: 443},
		},
		slug:          "feature",
		mainSlug:      "main",
		names:         identity.Derive("shop", "feature"),
		statefulModes: map[string]string{},
	}

	artifact, err := envArtifact(r, model)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Env["MINIO_PUBLIC_URL"] != "https://minio.shop.localhost" {
		t.Fatalf("MINIO_PUBLIC_URL = %q, want https://minio.shop.localhost", artifact.Env["MINIO_PUBLIC_URL"])
	}
}

func TestEnvArtifactRejectsPublicURLEnvCollision(t *testing.T) {
	model, err := compose.ParseConfigJSON([]byte(`{
		"services": {"minio": {"image": "minio/minio", "ports": [{"target": 9000, "published": "${MINIO_PORT:-9000}"}]}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	r := &resolved{
		manifest: &config.Config{
			App:    "shop",
			Shared: []string{"minio"},
			Services: map[string]config.Service{
				"minio": {Expose: "http", ProxyPort: 9000, PublicURLEnv: "MINIO_PUBLIC_URL"},
			},
			Stateful: map[string]config.Stateful{},
			Env:      map[string]string{"MINIO_PUBLIC_URL": "http://manually-set"},
			Proxy:    config.Proxy{Enabled: true, DNSSuffix: "localhost", HTTPSPort: 443},
		},
		slug:          "feature",
		mainSlug:      "main",
		names:         identity.Derive("shop", "feature"),
		statefulModes: map[string]string{},
	}

	_, err = envArtifact(r, model)
	if err == nil || !strings.Contains(err.Error(), "public_url_env") {
		t.Fatalf("envArtifact error = %v, want public_url_env collision guard", err)
	}
}

func TestEnvArtifactRejectsDuplicateIsolatedStatefulEnvKeys(t *testing.T) {
	r := &resolved{
		manifest: &config.Config{
			App: "shop",
			Env: map[string]string{},
			Stateful: map[string]config.Stateful{
				"postgres": {
					Engine: "postgres",
					Env: map[string]string{
						"STATEFUL_NAME": "postgres_{slug}",
					},
				},
				"redis": {
					Engine: "redis",
					Env: map[string]string{
						"STATEFUL_NAME": "redis_{slug}",
					},
				},
			},
		},
		slug:          "feature_one",
		mainSlug:      "main",
		names:         identity.Derive("shop", "feature_one"),
		statefulModes: map[string]string{"postgres": "isolated", "redis": "isolated"},
	}

	_, err := envArtifact(r, nil)
	if err == nil || !strings.Contains(err.Error(), "duplicate stateful env key STATEFUL_NAME") {
		t.Fatalf("envArtifact error = %v, want duplicate key guard", err)
	}
}
