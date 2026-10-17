package provider

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// writeFunction lays out a built function bundle: its manifest, its module,
// and one asset holding asset.
func writeFunction(t *testing.T, dir, description, asset string) {
	t.Helper()
	files := map[string]string{
		"subako-package.json":   fmt.Sprintf(`{"abi": 1, "name": "words", "description": %q}`, description),
		"module.wasm":           "\x00asm\x01\x00\x00\x00",
		"assets/data/words.txt": asset,
	}
	for rel, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// fullAgentHCL is an agent setting every part of the config, granting the
// function package in dir.
func fullAgentHCL(dir string) string {
	return fmt.Sprintf(`
resource "subako_function" "f" {
  source_dir = %q
}

resource "subako_agent" "a" {
  name              = "everything"
  model_provider_id = %q
  system_prompt     = "be brief"

  model = {
    anthropic = {
      name           = "claude-sonnet-5"
      max_tokens     = 8192
      context_window = 1000000
      image_input    = "unsupported"
      thinking       = { adaptive = {} }
      effort         = "high"
    }
  }

  sandboxes = [{
    name           = "build"
    environment    = { platform = { release = "2026" } }
    network        = "none"
    default_policy = "require_approval"
    tools          = [{ name = "exec", policy = "allow" }]
  }]

  functions = [
    {
      name           = "files"
      source         = { platform = { package = "files" } }
      default_policy = "allow"
    },
    {
      name             = "words"
      source           = { workspace = { function_id = subako_function.f.id, version = 1 } }
      default_policy   = "deny"
      timeout_secs     = 120
      network          = { allowlist = { hosts = ["api.example.test"] } }
      http_credentials = ["https://api.example.test"]
    },
  ]

  volume = { data = "enabled" }

  builtin = {
    session_display_name = "until_user_edits"
    ask_user = {
      widgets = [
        { select = {} },
        { custom = {
          id          = "date"
          description = "Pick a date."
          props       = jsonencode({ type = "object", properties = { min = { type = "string" } } })
          answer      = jsonencode({ type = "string", format = "date" })
        } },
      ]
    }
  }
}
`, dir, platformProviderID)
}

// latestConfig is the config the agent's newest version carries.
func latestConfig(api *fakeAPI, s *terraform.State) (map[string]any, error) {
	rs := s.RootModule().Resources["subako_agent.a"]
	api.mu.Lock()
	defer api.mu.Unlock()
	agent, ok := api.agents[rs.Primary.ID]
	if !ok {
		return nil, fmt.Errorf("agent %s is not on the server", rs.Primary.ID)
	}
	return agent.Versions[len(agent.Versions)-1]["config"].(map[string]any), nil
}

// checkLatestConfig compares one field of the newest version's config, as
// JSON.
func checkLatestConfig(api *fakeAPI, field string, want any) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		config, err := latestConfig(api, s)
		if err != nil {
			return err
		}
		got, _ := json.Marshal(config[field])
		wanted, _ := json.Marshal(want)
		if string(got) != string(wanted) {
			return fmt.Errorf("the version holds %s %s, want %s", field, got, wanted)
		}
		return nil
	}
}

// Every part of the config is published as written and reads back as the
// same plan, so a second plan is empty and an import sees what was applied.
func TestAFullConfigReadsBackAsWritten(t *testing.T) {
	api, server := newFakeAPI(t)
	dir := t.TempDir()
	writeFunction(t, dir, "Looks words up.", "hello\n")
	config := providerBlock(server.URL, api.workspaceID) + fullAgentHCL(dir)

	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   versionChecks,
		ProtoV6ProviderFactories: factories,
		CheckDestroy:             checkEmpty(api),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					checkVersionCount(api, 1),
					checkLatestConfig(api, "sandboxes", []any{map[string]any{
						"name": "build", "environment": map[string]any{"type": "platform", "release": "2026"},
						"network": map[string]any{"type": "none"}, "default_policy": "require_approval",
						"tools": []any{map[string]any{"name": "exec", "policy": "allow"}},
					}}),
					checkLatestConfig(api, "volume", map[string]any{"data": map[string]any{"type": "enabled"}}),
					func(s *terraform.State) error {
						config, err := latestConfig(api, s)
						if err != nil {
							return err
						}
						functions := config["functions"].([]any)
						files, _ := json.Marshal(functions[0])
						if want := `{"default_policy":"allow","http_credentials":[],"name":"files","network":{"type":"none"},` +
							`"source":{"package":"files","type":"platform"},"timeout_secs":null,"tools":[]}`; string(files) != want {
							return fmt.Errorf("the files grant is %s, want %s", files, want)
						}
						words := functions[1].(map[string]any)
						source := words["source"].(map[string]any)
						if source["type"] != "workspace" || source["function_id"] != s.RootModule().Resources["subako_function.f"].Primary.ID {
							return fmt.Errorf("the words grant names source %v", source)
						}
						model, _ := json.Marshal(config["model"])
						if want := `{"context_window":1000000,"effort":"high","format":"anthropic","image_input":"unsupported",` +
							`"max_tokens":8192,"model":"claude-sonnet-5","thinking":{"type":"adaptive"}}`; string(model) != want {
							return fmt.Errorf("the model is %s, want %s", model, want)
						}
						return nil
					},
				),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("subako_agent.a",
						tfjsonpath.New("functions").AtSliceIndex(0).AtMapKey("network").AtMapKey("none"),
						knownvalue.MapExact(map[string]knownvalue.Check{})),
					statecheck.ExpectKnownValue("subako_agent.a",
						tfjsonpath.New("mcp_servers"), knownvalue.ListExact([]knownvalue.Check{})),
				},
			},
			{
				Config:   config,
				PlanOnly: true,
			},
			{
				ResourceName:      "subako_agent.a",
				ImportState:       true,
				ImportStateVerify: true,
			},
		},
	})
}

// minimalAgentHCL is an agent granting nothing, carrying the lifecycle and
// prompt given.
func minimalAgentHCL(prompt, lifecycle string) string {
	return fmt.Sprintf(`
resource "subako_agent" "a" {
  name              = "support"
  model_provider_id = %q
  system_prompt     = %q
  model             = { anthropic = { name = "claude-sonnet-5", max_tokens = 8192, context_window = 1000000 } }
%s
}
`, platformProviderID, prompt, lifecycle)
}

// grantSandboxElsewhere publishes a version granting a sandbox, as the
// console would.
func grantSandboxElsewhere(api *fakeAPI) func() {
	return func() {
		api.mu.Lock()
		defer api.mu.Unlock()
		for _, agent := range api.agents {
			latest := agent.Versions[len(agent.Versions)-1]
			config := map[string]any{}
			for k, v := range latest["config"].(map[string]any) {
				config[k] = v
			}
			config["sandboxes"] = []any{map[string]any{
				"name": "console", "environment": map[string]any{"type": "platform", "release": "2026"},
				"network": map[string]any{"type": "unrestricted"}, "default_policy": "allow", "tools": []any{},
			}}
			edited := map[string]any{}
			for k, v := range latest {
				edited[k] = v
			}
			edited["config"] = config
			edited["id"] = newID()
			edited["version"] = len(agent.Versions) + 1
			agent.Versions = append(agent.Versions, edited)
		}
	}
}

// Terraform owns the whole config, so a grant added elsewhere is undone by
// the next apply -- unless the configuration leaves it to that other tool,
// in which case a publish carries it along.
func TestAGrantAddedElsewhereIsUndoneUnlessIgnored(t *testing.T) {
	api, server := newFakeAPI(t)
	head := providerBlock(server.URL, api.workspaceID)
	ignoring := `  lifecycle {
    ignore_changes = [sandboxes]
  }`

	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   versionChecks,
		ProtoV6ProviderFactories: factories,
		CheckDestroy:             checkEmpty(api),
		Steps: []resource.TestStep{
			{Config: head + minimalAgentHCL("be brief", "")},
			{
				PreConfig: grantSandboxElsewhere(api),
				Config:    head + minimalAgentHCL("be brief", ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectUnknownValue("subako_agent.a", tfjsonpath.New("version")),
						plancheck.ExpectKnownValue("subako_agent.a", tfjsonpath.New("sandboxes"),
							knownvalue.ListExact([]knownvalue.Check{})),
					},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					checkVersionCount(api, 3),
					checkLatestConfig(api, "sandboxes", []any{}),
				),
			},
			{
				PreConfig: grantSandboxElsewhere(api),
				Config:    head + minimalAgentHCL("be thorough", ignoring),
				Check: resource.ComposeAggregateTestCheckFunc(
					checkVersionCount(api, 5),
					checkLatestConfig(api, "system_prompt", "be thorough"),
					func(s *terraform.State) error {
						config, err := latestConfig(api, s)
						if err != nil {
							return err
						}
						if sandboxes := config["sandboxes"].([]any); len(sandboxes) != 1 {
							return fmt.Errorf("the version holds sandboxes %v, want the one granted elsewhere", sandboxes)
						}
						return nil
					},
				),
			},
		},
	})
}

func thinkingHCL(thinking string) string {
	return fmt.Sprintf(`
resource "subako_agent" "a" {
  name              = "support"
  model_provider_id = %q
  model = {
    anthropic = {
      name           = "claude-sonnet-5"
      max_tokens     = 8192
      context_window = 1000000
      %s
    }
  }
}
`, platformProviderID, thinking)
}

func checkLatestThinking(api *fakeAPI, want any) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		config, err := latestConfig(api, s)
		if err != nil {
			return err
		}
		got, _ := json.Marshal(config["model"].(map[string]any)["thinking"])
		wanted, _ := json.Marshal(want)
		if string(got) != string(wanted) {
			return fmt.Errorf("the version thinks %s, want %s", got, wanted)
		}
		return nil
	}
}

func TestEachThinkingVariantPublishesItsKind(t *testing.T) {
	api, server := newFakeAPI(t)
	head := providerBlock(server.URL, api.workspaceID)
	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   versionChecks,
		ProtoV6ProviderFactories: factories,
		CheckDestroy:             checkEmpty(api),
		Steps: []resource.TestStep{
			{
				Config: head + thinkingHCL(`thinking = { enabled = { budget_tokens = 2048 } }`),
				Check:  checkLatestThinking(api, map[string]any{"type": "enabled", "budget_tokens": 2048}),
			},
			{
				Config: head + thinkingHCL(`thinking = { between_tools = {} }`),
				Check:  checkLatestThinking(api, map[string]any{"type": "between_tools"}),
			},
			{
				Config: head + thinkingHCL(`thinking = { disabled = {} }`),
				Check:  checkLatestThinking(api, map[string]any{"type": "disabled"}),
			},
			{
				Config: head + thinkingHCL(""),
				Check: resource.ComposeAggregateTestCheckFunc(
					checkLatestThinking(api, nil),
					checkVersionCount(api, 4),
				),
			},
		},
	})
}

// A version holding a field this provider has no attribute for cannot be
// planned against: the next publish would drop it without the plan showing.
func TestAVersionHoldingAnUnknownFieldFailsThePlan(t *testing.T) {
	api, server := newFakeAPI(t)
	config := providerBlock(server.URL, api.workspaceID) + minimalAgentHCL("be brief", "")
	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   versionChecks,
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() {
					api.mu.Lock()
					defer api.mu.Unlock()
					for _, agent := range api.agents {
						agent.Versions[len(agent.Versions)-1]["config"].(map[string]any)["subagent"] = map[string]any{}
					}
				},
				Config:      config,
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`unknown field "subagent"`),
			},
			{
				// Taken back out, so the agent can be destroyed.
				PreConfig: func() {
					api.mu.Lock()
					defer api.mu.Unlock()
					for _, agent := range api.agents {
						delete(agent.Versions[len(agent.Versions)-1]["config"].(map[string]any), "subagent")
					}
				},
				Config: config,
			},
		},
	})
}

func functionHCL(dir string) string {
	return fmt.Sprintf(`
resource "subako_function" "f" {
  source_dir = %q
}
`, dir)
}

func checkFunctionVersions(api *fakeAPI, want int) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs := s.RootModule().Resources["subako_function.f"]
		api.mu.Lock()
		defer api.mu.Unlock()
		function, ok := api.functions[rs.Primary.ID]
		if !ok {
			return fmt.Errorf("function %s is not on the server", rs.Primary.ID)
		}
		if got := len(function.Versions); got != want {
			return fmt.Errorf("the server holds %d versions, want %d", got, want)
		}
		return nil
	}
}

// A function package's latest version fingerprints as the directory pushed
// it, so only a change to the manifest, the module, or an asset pushes.
func TestChangingAFunctionsFilesPushesAVersion(t *testing.T) {
	api, server := newFakeAPI(t)
	dir := t.TempDir()
	writeFunction(t, dir, "Looks words up.", "hello\n")
	config := providerBlock(server.URL, api.workspaceID) + functionHCL(dir)

	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   versionChecks,
		ProtoV6ProviderFactories: factories,
		CheckDestroy:             checkEmpty(api),
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("subako_function.f", tfjsonpath.New("name"), knownvalue.StringExact("words")),
					statecheck.ExpectKnownValue("subako_function.f", tfjsonpath.New("latest_version"), knownvalue.Int64Exact(1)),
				},
				Check: checkFunctionVersions(api, 1),
			},
			{
				Config: config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("subako_function.f", plancheck.ResourceActionNoop),
					},
				},
			},
			{
				PreConfig: func() { writeFunction(t, dir, "Looks words up.", "goodbye\n") },
				Config:    config,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("subako_function.f", plancheck.ResourceActionUpdate),
						plancheck.ExpectUnknownValue("subako_function.f", tfjsonpath.New("latest_version")),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("subako_function.f", tfjsonpath.New("latest_version"), knownvalue.Int64Exact(2)),
				},
				Check: checkFunctionVersions(api, 2),
			},
			{
				PreConfig: func() { writeFunction(t, dir, "Looks words up, fast.", "goodbye\n") },
				Config:    config,
				Check:     checkFunctionVersions(api, 3),
			},
			{
				ResourceName:            "subako_function.f",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"source_dir"},
			},
		},
	})
}

func TestAFunctionDirectoryHoldingAnythingElseFailsThePlan(t *testing.T) {
	api, server := newFakeAPI(t)
	dir := t.TempDir()
	writeFunction(t, dir, "d", "a")
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[package]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   versionChecks,
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config:      providerBlock(server.URL, api.workspaceID) + functionHCL(dir),
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`is not part of a function bundle`),
		}},
	})
}
