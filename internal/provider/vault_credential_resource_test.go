package provider

import (
	"context"
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// httpCredentialHCL is an http credential sent to origin as one
// authorization header.
func httpCredentialHCL(name, origin, header, value string, version int) string {
	return fmt.Sprintf(`
resource "subako_vault" "v" {
  display_name = "functions"
}

resource "subako_vault_credential" %q {
  vault_id           = subako_vault.v.id
  display_name       = "GitHub API"
  secrets_wo_version = %d

  http = {
    origins = [%q, "https://uploads.example.test"]
    headers = [
      { name = %q, value_wo = %q },
      { name = "x-api-version", value_wo = "2026-01-01" },
    ]
  }
}
`, name, version, origin, header, value)
}

func checkHTTPSecret(api *fakeAPI, resourceName, want string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		id := s.RootModule().Resources[resourceName].Primary.ID
		api.mu.Lock()
		defer api.mu.Unlock()
		headers, _ := api.secrets[id]["headers"].([]any)
		if len(headers) != 2 || fmt.Sprint(headers[0].(map[string]any)["value"]) != want {
			return fmt.Errorf("the server holds headers %v, want the first valued %q", headers, want)
		}
		return nil
	}
}

func TestAnHTTPCredentialIsSentAsHeadersToItsOrigins(t *testing.T) {
	api, server := newFakeAPI(t)
	head := providerBlock(server.URL, api.workspaceID)

	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   versionChecks,
		ProtoV6ProviderFactories: factories,
		CheckDestroy:             checkEmpty(api),
		Steps: []resource.TestStep{
			{
				Config: head + httpCredentialHCL("c", "https://api.example.test", "authorization", "Bearer one", 1),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("subako_vault_credential.c",
						tfjsonpath.New("http").AtMapKey("headers").AtSliceIndex(0).AtMapKey("value_wo"), knownvalue.Null()),
					statecheck.ExpectKnownValue("subako_vault_credential.c", tfjsonpath.New("mcp"), knownvalue.Null()),
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					checkHTTPSecret(api, "subako_vault_credential.c", "Bearer one"),
					func(s *terraform.State) error {
						id := s.RootModule().Resources["subako_vault_credential.c"].Primary.ID
						api.mu.Lock()
						defer api.mu.Unlock()
						if p := api.secrets[id]; p["auth_scheme"] != "header" {
							return fmt.Errorf("the server holds payload %v", p)
						}
						return nil
					},
				),
			},
			{
				// A new value is sent by bumping the version, which replaces
				// the credential.
				Config: head + httpCredentialHCL("c", "https://api.example.test", "authorization", "Bearer two", 2),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("subako_vault_credential.c", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
				Check: checkHTTPSecret(api, "subako_vault_credential.c", "Bearer two"),
			},
			{
				ResourceName:            "subako_vault_credential.c",
				ImportState:             true,
				ImportStateIdFunc:       credentialImportID,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"secrets_wo_version"},
			},
		},
	})
}

// The server stores an origin and a header name in its own spelling, which
// the state does not take over from the configuration's.
func TestAnHTTPCredentialSpelledAnotherWayPlansNoChange(t *testing.T) {
	api, server := newFakeAPI(t)
	config := providerBlock(server.URL, api.workspaceID) +
		httpCredentialHCL("c", "https://API.Example.test:443/", "Authorization", "Bearer one", 1)

	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   versionChecks,
		ProtoV6ProviderFactories: factories,
		CheckDestroy:             checkEmpty(api),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: func(s *terraform.State) error {
					id := s.RootModule().Resources["subako_vault_credential.c"].Primary.ID
					api.mu.Lock()
					defer api.mu.Unlock()
					for _, vault := range api.vaults {
						for _, c := range vault.Credentials {
							if c["id"] == id && fmt.Sprint(c["origins"]) != "[https://api.example.test https://uploads.example.test]" {
								return fmt.Errorf("the server holds origins %v", c["origins"])
							}
						}
					}
					return nil
				},
			},
			{Config: config, PlanOnly: true},
		},
	})
}

func TestAnOriginAnotherCredentialIsSentToIsRefused(t *testing.T) {
	api, server := newFakeAPI(t)
	second := `
resource "subako_vault_credential" "d" {
  vault_id           = subako_vault.v.id
  secrets_wo_version = 1
  http = {
    origins = ["https://api.example.test"]
    headers = [{ name = "authorization", value_wo = "Bearer other" }]
  }
}
`
	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   versionChecks,
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{Config: providerBlock(server.URL, api.workspaceID) + httpCredentialHCL("c", "https://api.example.test", "authorization", "t", 1)},
			{
				Config:      providerBlock(server.URL, api.workspaceID) + httpCredentialHCL("c", "https://api.example.test", "authorization", "t", 1) + second,
				ExpectError: regexp.MustCompile(`(?s)another credential in the vault is sent to.*https://api\.example\.test`),
			},
		},
	})
}

// A credential connected through an OAuth sign-in cannot be recreated from a
// configuration, so it is refused rather than adopted.
func TestACredentialConnectedThroughOAuthIsNotAdopted(t *testing.T) {
	api, server := newFakeAPI(t)
	head := providerBlock(server.URL, api.workspaceID)
	connected := newID()
	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   versionChecks,
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{
			{
				Config: head + `
resource "subako_vault" "v" {
  display_name = "functions"
}
`,
				Check: func(s *terraform.State) error {
					vaultID := s.RootModule().Resources["subako_vault.v"].Primary.ID
					api.mu.Lock()
					defer api.mu.Unlock()
					vault := api.vaults[vaultID]
					vault.Credentials = append(vault.Credentials, map[string]any{
						"id": connected, "vault_id": vaultID, "protocol": "http", "auth_scheme": "oauth",
						"origins": []any{"https://www.googleapis.com"}, "headers": []any{map[string]any{"name": "authorization"}},
						"oauth_client_id": newID(), "display_name": "Drive", "status": "active",
					})
					return nil
				},
			},
			{
				Config: head + `
resource "subako_vault" "v" {
  display_name = "functions"
}

resource "subako_vault_credential" "c" {
  vault_id           = subako_vault.v.id
  secrets_wo_version = 1
  http = {
    origins = ["https://www.googleapis.com"]
    headers = [{ name = "authorization", value_wo = "t" }]
  }
}
`,
				ResourceName: "subako_vault_credential.c",
				ImportState:  true,
				ImportStateIdFunc: func(s *terraform.State) (string, error) {
					return s.RootModule().Resources["subako_vault.v"].Primary.ID + "/" + connected, nil
				},
				ExpectError: regexp.MustCompile(`Credential connected through OAuth`),
			},
			{
				// Removed again, so the vault can be destroyed.
				PreConfig: func() {
					api.mu.Lock()
					defer api.mu.Unlock()
					for _, vault := range api.vaults {
						vault.Credentials = nil
					}
				},
				Config: head + `
resource "subako_vault" "v" {
  display_name = "functions"
}
`,
			},
		},
	})
}

// A version 0 state, whose MCP target and auth schemes sat at the top level,
// upgrades to one the current schema decodes.
func TestAVersion0CredentialStateUpgrades(t *testing.T) {
	v0 := `{
		"id": "c", "vault_id": "v", "target": "https://mcp.example.test/", "display_name": "GitHub",
		"static_bearer": {"token_wo": null}, "oauth": null, "secrets_wo_version": 1
	}`
	upgraded, err := upgradeVaultCredentialJSONV0([]byte(v0))
	if err != nil {
		t.Fatal(err)
	}
	stateType := vaultCredentialSchema().Type().TerraformType(context.Background())
	value, err := tftypes.ValueFromJSONWithOpts(upgraded, stateType, tftypes.ValueFromJSONOpts{})
	if err != nil {
		t.Fatalf("the upgraded state does not decode: %v\n%s", err, upgraded)
	}
	var state map[string]tftypes.Value
	_ = value.As(&state)
	var mcp map[string]tftypes.Value
	_ = state["mcp"].As(&mcp)
	var target string
	_ = mcp["target"].As(&target)
	if target != "https://mcp.example.test/" || !state["http"].IsNull() {
		t.Fatalf("upgraded to %s", upgraded)
	}
}
