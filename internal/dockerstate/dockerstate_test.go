package dockerstate

import "testing"

func TestParseDockerRuntimeStateIntoStacksServicesPortsAndForkModes(t *testing.T) {
	ls, err := ParseComposeLSJSON([]byte(`[
		{"Name":"shop-feature_one","Status":"running(2)"},
		{"Name":"shop-infra","Status":"running(1)"}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	services, err := ParseDockerPSJSON([]byte(`[
		{
			"Name":"shop-feature_one-api-1",
			"Image":"shop-api",
			"State":"running",
			"Status":"Up 2 minutes",
			"Labels":{
				"com.docktree.app":"shop",
				"com.docktree.slug":"feature_one",
				"com.docktree.project":"shop-feature_one",
				"com.docktree.service":"api",
				"com.docktree.tier":"worktree"
			},
			"Ports":"0.0.0.0:18080->3000/tcp"
		},
		{
			"Name":"shop-infra-postgres-1",
			"Image":"postgres:16",
			"State":"running",
			"Status":"Up 10 minutes",
			"Labels":"com.docktree.app=shop,com.docktree.slug=main,com.docktree.project=shop-infra,com.docktree.service=postgres,com.docktree.tier=infra,com.docktree.fork=postgres",
			"Ports":"5432->5432/tcp"
		}
	]`))
	if err != nil {
		t.Fatal(err)
	}

	stacks := Merge(ls, services)
	if len(stacks) != 2 {
		t.Fatalf("stacks = %#v, want two", stacks)
	}
	api, ok := FindService(stacks, "feature_one", "api")
	if !ok {
		t.Fatalf("api service missing from %#v", stacks)
	}
	if api.App != "shop" || api.Project != "shop-feature_one" || api.State != "running" || api.ForkMode != "shared" {
		t.Fatalf("api = %#v", api)
	}
	if len(api.Ports) != 1 || api.Ports[0].Published != 18080 || api.Ports[0].Target != 3000 || api.Ports[0].URL != "http://localhost:18080" {
		t.Fatalf("api ports = %#v", api.Ports)
	}
	pg, ok := FindService(stacks, "main", "postgres")
	if !ok {
		t.Fatalf("postgres service missing from %#v", stacks)
	}
	if pg.ForkMode != "isolated" || pg.Tier != "infra" {
		t.Fatalf("postgres = %#v", pg)
	}
	if url, ok := FindURL(stacks, "feature_one", "api"); !ok || url != "http://localhost:18080" {
		t.Fatalf("url = %q/%v, want api URL", url, ok)
	}
}

func TestParsesLineDelimitedDockerJSONAndPublisherMaps(t *testing.T) {
	ls, err := ParseComposeLSJSON([]byte(`{"Name":"shop-feature_one","Status":"running(1)"}
{"Name":"shop-infra","Status":"running(2)"}
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(ls) != 2 || ls[0].Project != "shop-feature_one" || ls[1].Project != "shop-infra" {
		t.Fatalf("compose ls = %#v", ls)
	}

	services, err := ParseDockerPSJSON([]byte(`[
		{
			"Name":"fallback-container",
			"Image":"example/api",
			"State":"running",
			"Labels":{
				"docktree.app":"shop",
				"docktree.slug":"feature_one",
				"com.docker.compose.project":"shop-feature_one",
				"com.docker.compose.service":"api",
				"docktree.data":"isolated"
			},
			"Ports":[
				{"TargetPort":3000,"PublishedPort":18080,"URL":"127.0.0.1","Protocol":"tcp"}
			]
		}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 {
		t.Fatalf("services = %#v", services)
	}
	got := services[0]
	if got.App != "shop" || got.Slug != "feature_one" || got.Project != "shop-feature_one" || got.Name != "api" {
		t.Fatalf("service identity = %#v", got)
	}
	if got.ForkMode != "isolated" {
		t.Fatalf("fork mode = %q, want isolated", got.ForkMode)
	}
	if len(got.Ports) != 1 || got.Ports[0].HostIP != "127.0.0.1" || got.Ports[0].URL != "http://127.0.0.1:18080" {
		t.Fatalf("ports = %#v", got.Ports)
	}
}

func TestParseComposePSJSONPublishedPorts(t *testing.T) {
	services, err := ParseComposePSJSON([]byte(`[
		{
			"Service":"api",
			"State":"running",
			"Health":"healthy",
			"Image":"shop-api",
			"Publishers":[
				{"URL":"0.0.0.0","TargetPort":3000,"PublishedPort":18080,"Protocol":"tcp"}
			]
		}
	]`), Identity{App: "shop", Slug: "feature_one", Project: "shop-feature_one"})
	if err != nil {
		t.Fatal(err)
	}
	if len(services) != 1 {
		t.Fatalf("services = %#v", services)
	}
	got := services[0]
	if got.App != "shop" || got.Slug != "feature_one" || got.Project != "shop-feature_one" || got.Name != "api" || got.Health != "healthy" {
		t.Fatalf("service = %#v", got)
	}
	if len(got.Ports) != 1 || got.Ports[0].URL != "http://localhost:18080" {
		t.Fatalf("ports = %#v", got.Ports)
	}
}

func TestDoesNotSynthesizeHTTPURLsForNonHTTPPorts(t *testing.T) {
	services, err := ParseDockerPSJSON([]byte(`[
		{
			"Name":"shop-infra-postgres-1",
			"State":"running",
			"Labels":"com.docktree.app=shop,com.docktree.slug=main,com.docktree.project=shop-infra,com.docktree.service=postgres",
			"Ports":"0.0.0.0:15432->5432/tcp"
		},
		{
			"Name":"shop-infra-redis-1",
			"State":"running",
			"Labels":"com.docktree.app=shop,com.docktree.slug=main,com.docktree.project=shop-infra,com.docktree.service=redis",
			"Ports":"0.0.0.0:16379->6379/tcp"
		}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	for _, service := range services {
		if len(service.Ports) != 1 {
			t.Fatalf("service = %#v, want one port", service)
		}
		if service.Ports[0].URL != "" {
			t.Fatalf("%s URL = %q, want empty for non-HTTP port", service.Name, service.Ports[0].URL)
		}
	}
}

func TestRepresentativeRuntimeStatusRegression(t *testing.T) {
	ls, err := ParseComposeLSJSON([]byte(`[
		{"Name":"shop-feature_one","Status":"running(3)"},
		{"Name":"shop-infra","Status":"running(2)"}
	]`))
	if err != nil {
		t.Fatal(err)
	}
	services, err := ParseDockerPSJSON([]byte(`[
		{
			"Name":"shop-feature_one-api-1",
			"Image":"example/api:latest",
			"State":"running",
			"Status":"Up 1 minute",
			"Labels":{
				"com.docktree.app":"shop",
				"com.docktree.slug":"feature_one",
				"com.docktree.project":"shop-feature_one",
				"com.docktree.service":"api",
				"com.docktree.tier":"worktree"
			},
			"Ports":"0.0.0.0:18080->3000/tcp"
		},
		{
			"Name":"shop-feature_one-frontend-1",
			"Image":"example/frontend:latest",
			"State":"running",
			"Status":"Up 1 minute",
			"Labels":{
				"com.docktree.app":"shop",
				"com.docktree.slug":"feature_one",
				"com.docktree.project":"shop-feature_one",
				"com.docktree.service":"frontend",
				"com.docktree.tier":"worktree"
			},
			"Ports":"0.0.0.0:15173->5173/tcp"
		},
		{
			"Name":"shop-feature_one-e2e-1",
			"Image":"example/e2e:latest",
			"State":"exited",
			"Status":"Exited (0)",
			"Labels":{
				"com.docktree.app":"shop",
				"com.docktree.slug":"feature_one",
				"com.docktree.project":"shop-feature_one",
				"com.docktree.service":"e2e",
				"com.docktree.tier":"worktree"
			},
			"Ports":""
		},
		{
			"Name":"shop-infra-postgres-1",
			"Image":"postgres:16",
			"State":"running",
			"Status":"Up 5 minutes",
			"Labels":"com.docktree.app=shop,com.docktree.slug=main,com.docktree.project=shop-infra,com.docktree.service=postgres,com.docktree.tier=infra",
			"Ports":"0.0.0.0:15432->5432/tcp"
		},
		{
			"Name":"shop-infra-redis-1",
			"Image":"redis:7",
			"State":"running",
			"Status":"Up 5 minutes",
			"Labels":"com.docktree.app=shop,com.docktree.slug=main,com.docktree.project=shop-infra,com.docktree.service=redis,com.docktree.tier=infra",
			"Ports":"0.0.0.0:16379->6379/tcp"
		}
	]`))
	if err != nil {
		t.Fatal(err)
	}

	stacks := Merge(ls, services)
	if len(stacks) != 2 {
		t.Fatalf("stacks = %#v, want worktree and infra", stacks)
	}
	worktree := findStack(t, stacks, "shop-feature_one")
	if worktree.App != "shop" || worktree.Slug != "feature_one" || worktree.Status != "running(3)" {
		t.Fatalf("worktree stack = %#v", worktree)
	}
	if len(worktree.Services) != 3 {
		t.Fatalf("worktree services = %#v, want api, frontend, and e2e", worktree.Services)
	}
	if url, ok := FindURL(stacks, "feature_one", "api"); !ok || url != "http://localhost:18080" {
		t.Fatalf("api URL = %q/%v, want http://localhost:18080", url, ok)
	}
	if url, ok := FindURL(stacks, "feature_one", "frontend"); !ok || url != "http://localhost:15173" {
		t.Fatalf("frontend URL = %q/%v, want http://localhost:15173", url, ok)
	}
	e2e, ok := FindService(stacks, "feature_one", "e2e")
	if !ok || e2e.State != "exited" || len(e2e.Ports) != 0 {
		t.Fatalf("e2e service = %#v/%v", e2e, ok)
	}
	infra := findStack(t, stacks, "shop-infra")
	if infra.Slug != "main" || infra.Status != "running(2)" || len(infra.Services) != 2 {
		t.Fatalf("infra stack = %#v", infra)
	}
	postgres, ok := FindService(stacks, "main", "postgres")
	if !ok || postgres.Tier != "infra" || postgres.ForkMode != "shared" {
		t.Fatalf("postgres service = %#v/%v", postgres, ok)
	}
	redis, ok := FindService(stacks, "main", "redis")
	if !ok || redis.Tier != "infra" || len(redis.Ports) != 1 || redis.Ports[0].Published != 16379 {
		t.Fatalf("redis service = %#v/%v", redis, ok)
	}
}

func findStack(t *testing.T, stacks []Stack, project string) Stack {
	t.Helper()
	for _, stack := range stacks {
		if stack.Project == project {
			return stack
		}
	}
	t.Fatalf("project %s missing from %#v", project, stacks)
	return Stack{}
}
