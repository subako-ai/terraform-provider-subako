package provider

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// federatedProviderHCL is a workspace provider authenticating with auth, an
// HCL object setting one variant.
func federatedProviderHCL(format, baseURL, name, auth string) string {
	return fmt.Sprintf(`
resource "subako_model_provider" "p" {
  format       = %q
  display_name = %q
  base_url     = %q
  auth         = %s
}
`, format, name, baseURL, auth)
}

const (
	roleARN         = "arn:aws:iam::123456789012:role/subako-bedrock"
	bedrockURL      = "https://bedrock-runtime.us-east-1.amazonaws.com/anthropic"
	anthropicOrgID  = "6f1c2a7e-3b4d-4e5f-8a9b-0c1d2e3f4a5b"
	awsRoleAuthHCL  = `{ aws_web_identity = { role_arn = "` + roleARN + `", region = "us-east-1" } }`
	anthropicHCL    = `{ anthropic_workload_identity = { organization_id = "` + anthropicOrgID + `", service_account_id = "svac_01Prod", federation_rule_id = "fdrl_01Prod" } }`
	openaiHCL       = `{ openai_workload_identity = { identity_provider_id = "idp_01Prod", service_account_id = "user-01Prod" } }`
	anthropicAPIURL = "https://api.anthropic.com"
	openaiAPIURL    = "https://api.openai.com/v1"
)

// heldAuth pins the auth the server holds for the provider under test, as it
// answers it.
func heldAuth(api *fakeAPI, want string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		id := s.RootModule().Resources[modelProviderAddress].Primary.ID
		api.mu.Lock()
		defer api.mu.Unlock()
		got, err := json.Marshal(api.providers[id].Body["auth"])
		if err != nil {
			return err
		}
		if string(got) != want {
			return fmt.Errorf("provider %s holds auth %s, want %s", id, got, want)
		}
		return nil
	}
}

// providerHoldsNoKey pins that the server holds no key for the provider.
func providerHoldsNoKey(api *fakeAPI) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		id := s.RootModule().Resources[modelProviderAddress].Primary.ID
		api.mu.Lock()
		defer api.mu.Unlock()
		if key, held := api.apiKeys[id]; held {
			return fmt.Errorf("provider %s holds key %q", id, key)
		}
		return nil
	}
}

// authUpdates pins how many updates carried an auth of any kind.
func authUpdates(api *fakeAPI, want int) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		id := s.RootModule().Resources[modelProviderAddress].Primary.ID
		api.mu.Lock()
		defer api.mu.Unlock()
		if got := api.authUpdates[id]; got != want {
			return fmt.Errorf("provider %s took %d auth updates, want %d", id, got, want)
		}
		return nil
	}
}

func authAt(variant string, attribute string) tfjsonpath.Path {
	return tfjsonpath.New("auth").AtMapKey(variant).AtMapKey(attribute)
}

// A federated provider sends what its account trusts, holds no key, and reads
// its identity back from the server, so a second plan has nothing to do and
// an import recovers everything the API returns.
func TestAFederatedModelProviderReadsItsIdentityBack(t *testing.T) {
	cases := []struct {
		name, format, baseURL, auth, held string
		state                             []statecheck.StateCheck
	}{
		{
			name: "aws_web_identity", format: "anthropic", baseURL: bedrockURL, auth: awsRoleAuthHCL,
			held: `{"region":"us-east-1","role_arn":"` + roleARN + `","type":"aws_web_identity"}`,
			state: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(modelProviderAddress, authAt("aws_web_identity", "role_arn"), knownvalue.StringExact(roleARN)),
			},
		},
		{
			name: "anthropic_workload_identity", format: "anthropic", baseURL: anthropicAPIURL, auth: anthropicHCL,
			held: `{"federation_rule_id":"fdrl_01Prod","organization_id":"` + anthropicOrgID +
				`","service_account_id":"svac_01Prod","type":"anthropic_workload_identity","workspace_id":null}`,
			state: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(modelProviderAddress, authAt("anthropic_workload_identity", "workspace_id"), knownvalue.Null()),
			},
		},
		{
			name: "openai_workload_identity", format: "openai_responses", baseURL: openaiAPIURL, auth: openaiHCL,
			held: `{"identity_provider_id":"idp_01Prod","service_account_id":"user-01Prod","type":"openai_workload_identity"}`,
			state: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(modelProviderAddress, authAt("openai_workload_identity", "identity_provider_id"),
					knownvalue.StringExact("idp_01Prod")),
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			api, server := newFakeAPI(t)
			head := providerBlock(server.URL, api.workspaceID)
			config := head + federatedProviderHCL(c.format, c.baseURL, "Federated", c.auth)

			resource.UnitTest(t, resource.TestCase{
				TerraformVersionChecks:   versionChecks,
				ProtoV6ProviderFactories: factories,
				CheckDestroy:             checkEmpty(api),
				Steps: []resource.TestStep{
					{
						Config: config,
						ConfigStateChecks: append(c.state,
							statecheck.ExpectKnownValue(modelProviderAddress, tfjsonpath.New("auth").AtMapKey("api_key"), knownvalue.Null()),
							statecheck.ExpectKnownValue(modelProviderAddress, tfjsonpath.New("api_key_wo_version"), knownvalue.Null()),
						),
						Check: resource.ComposeAggregateTestCheckFunc(heldAuth(api, c.held), providerHoldsNoKey(api)),
					},
					{Config: config, PlanOnly: true},
					{
						ResourceName:      modelProviderAddress,
						Config:            config,
						ImportState:       true,
						ImportStateVerify: true,
						// The API does not return it, so no import recovers it.
						ImportStateVerifyIgnore: []string{"base_url"},
					},
				},
			})
		})
	}
}

// A provider moves between a key and a role in place: it keeps its id, and
// each change of auth is one update carrying the whole new auth. A rename
// carries none.
func TestAModelProviderSwitchesAuthInPlace(t *testing.T) {
	api, server := newFakeAPI(t)
	head := providerBlock(server.URL, api.workspaceID)
	var firstID string
	role := func(region string) string {
		return federatedProviderHCL("anthropic", bedrockURL, "Mine",
			`{ aws_web_identity = { role_arn = "`+roleARN+`", region = "`+region+`" } }`)
	}

	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   versionChecks,
		ProtoV6ProviderFactories: factories,
		CheckDestroy:             checkEmpty(api),
		Steps: []resource.TestStep{
			{
				Config: head + modelProviderHCL("Mine", "key-1", 1),
				Check:  recordProviderID(&firstID),
			},
			{
				Config:           head + role("us-east-1"),
				ConfigPlanChecks: expectAction(plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					sameProviderAs(&firstID),
					heldAuth(api, `{"region":"us-east-1","role_arn":"`+roleARN+`","type":"aws_web_identity"}`),
					authUpdates(api, 1),
					providerHoldsNoKey(api),
				),
			},
			{
				Config:           head + role("us-west-2"),
				ConfigPlanChecks: expectAction(plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					sameProviderAs(&firstID),
					heldAuth(api, `{"region":"us-west-2","role_arn":"`+roleARN+`","type":"aws_web_identity"}`),
					authUpdates(api, 2),
				),
			},
			{
				Config: head + federatedProviderHCL("anthropic", bedrockURL, "Renamed",
					`{ aws_web_identity = { role_arn = "`+roleARN+`", region = "us-west-2" } }`),
				ConfigPlanChecks: expectAction(plancheck.ResourceActionUpdate),
				Check:            authUpdates(api, 2),
			},
			{
				// Back to a key under the version the provider last held: the
				// auth itself changed, so the key travels anyway.
				Config:           head + modelProviderHCL("Renamed", "key-2", 1),
				ConfigPlanChecks: expectAction(plancheck.ResourceActionUpdate),
				Check: resource.ComposeAggregateTestCheckFunc(
					sameProviderAs(&firstID),
					heldAuth(api, `{"type":"api_key"}`),
					providerKeyOf(api, "key-2"),
					authUpdates(api, 3),
				),
			},
		},
	})
}

// An auth changed outside Terraform shows up in the next plan, which puts the
// configuration's back.
func TestAFederatedAuthChangedOutsideTerraformIsPutBack(t *testing.T) {
	api, server := newFakeAPI(t)
	head := providerBlock(server.URL, api.workspaceID)
	config := head + federatedProviderHCL("openai_responses", openaiAPIURL, "Mine", openaiHCL)
	held := `{"identity_provider_id":"idp_01Prod","service_account_id":"user-01Prod","type":"openai_workload_identity"}`

	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   versionChecks,
		ProtoV6ProviderFactories: factories,
		CheckDestroy:             checkEmpty(api),
		Steps: []resource.TestStep{
			{Config: config, Check: heldAuth(api, held)},
			{
				PreConfig: func() {
					api.mu.Lock()
					defer api.mu.Unlock()
					for _, p := range api.providers {
						if p.Body["type"] == "byok" {
							p.Body["auth"].(map[string]any)["service_account_id"] = "user-elsewhere"
						}
					}
				},
				Config:           config,
				ConfigPlanChecks: expectAction(plancheck.ResourceActionUpdate),
				Check:            heldAuth(api, held),
			},
		},
	})
}

func TestAModelProviderAuthIsCheckedBeforeAnyCall(t *testing.T) {
	api, server := newFakeAPI(t)
	head := providerBlock(server.URL, api.workspaceID)
	cases := []struct {
		name, config string
		err          *regexp.Regexp
	}{
		{
			name: "no way at all",
			config: `
resource "subako_model_provider" "p" {
  format       = "anthropic"
  display_name = "Mine"
  base_url     = "https://api.anthropic.com"
}
`,
			err: regexp.MustCompile(`"auth" is required`),
		},
		{
			name: "a key without its version",
			config: federatedProviderHCL("anthropic", anthropicAPIURL, "Mine",
				`{ api_key = { api_key_wo = "sk-1" } }`),
			err: regexp.MustCompile(`needs api_key_wo_version`),
		},
		{
			name:   "an Anthropic identity under the OpenAI format",
			config: federatedProviderHCL("openai_responses", openaiAPIURL, "Mine", anthropicHCL),
			err:    regexp.MustCompile(`speaks anthropic,\s+not openai_responses`),
		},
		{
			name:   "an OpenAI identity under the Anthropic format",
			config: federatedProviderHCL("anthropic", anthropicAPIURL, "Mine", openaiHCL),
			err:    regexp.MustCompile(`speaks\s+openai_responses,\s+not anthropic`),
		},
		{
			name: "two ways at once",
			config: federatedProviderHCL("anthropic", anthropicAPIURL, "Mine",
				`{ aws_web_identity = { role_arn = "`+roleARN+`", region = "us-east-1" }, api_key = { api_key_wo = "sk-1" } }`),
			err: regexp.MustCompile(`Only one of`),
		},
		{
			name: "an Anthropic id of another kind",
			config: federatedProviderHCL("anthropic", anthropicAPIURL, "Mine",
				`{ anthropic_workload_identity = { organization_id = "`+anthropicOrgID+`", service_account_id = "fdrl_01Prod", federation_rule_id = "fdrl_01Prod" } }`),
			err: regexp.MustCompile(`must be svac_\s+followed by letters and digits`),
		},
		{
			name: "an organization id spelled other than the API answers it",
			config: federatedProviderHCL("anthropic", anthropicAPIURL, "Mine",
				`{ anthropic_workload_identity = { organization_id = "6F1C2A7E-3B4D-4E5F-8A9B-0C1D2E3F4A5B", service_account_id = "svac_01Prod", federation_rule_id = "fdrl_01Prod" } }`),
			err: regexp.MustCompile(`lowercase hyphenated UUID`),
		},
		{
			name: "an OpenAI id with a space",
			config: federatedProviderHCL("openai_responses", openaiAPIURL, "Mine",
				`{ openai_workload_identity = { identity_provider_id = "idp 01", service_account_id = "user-01Prod" } }`),
			err: regexp.MustCompile(`letters,\s+digits, underscores and hyphens`),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				TerraformVersionChecks:   versionChecks,
				ProtoV6ProviderFactories: factories,
				Steps: []resource.TestStep{{
					Config:      head + c.config,
					PlanOnly:    true,
					ExpectError: c.err,
				}},
			})
		})
	}
}

// A version 0 state held its credential at the top level: a key's version
// beside the write-only key, or a role. Either now sits under `auth`.
func TestAVersion0ModelProviderStateMovesItsCredentialUnderAuth(t *testing.T) {
	const head = `"id":"p","format":"anthropic","display_name":"Mine","description":"","base_url":"https://api.anthropic.com",`
	cases := []struct{ name, v0, want string }{
		{
			name: "a key",
			v0:   `{` + head + `"api_key_wo":null,"api_key_wo_version":3}`,
			want: `{` + head + `"api_key_wo_version":3,"auth":{"api_key":{"api_key_wo":null},"aws_web_identity":null,
				"anthropic_workload_identity":null,"openai_workload_identity":null}}`,
		},
		{
			name: "a role",
			v0: `{` + head + `"api_key_wo":null,"api_key_wo_version":null,` +
				`"aws_web_identity":{"role_arn":"` + roleARN + `","region":"us-east-1"}}`,
			want: `{` + head + `"api_key_wo_version":null,"auth":{"api_key":null,
				"aws_web_identity":{"role_arn":"` + roleARN + `","region":"us-east-1"},
				"anthropic_workload_identity":null,"openai_workload_identity":null}}`,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			upgraded, err := upgradeModelProviderJSONV0([]byte(c.v0))
			if err != nil {
				t.Fatal(err)
			}
			var got, want map[string]any
			if err := json.Unmarshal(upgraded, &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(c.want), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("upgraded to %s", upgraded)
			}
		})
	}
}

// The data source hands an account what it trusts, so a role, a rule, or a
// mapping can be written in the same configuration as the provider.
func TestTheWorkloadIdentityDataSourceReadsWhatToTrust(t *testing.T) {
	api, server := newFakeAPI(t)
	head := providerBlock(server.URL, api.workspaceID)
	const address = "data.subako_workload_identity.this"

	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   versionChecks,
		ProtoV6ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config: head + `data "subako_workload_identity" "this" {}`,
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(address, tfjsonpath.New("issuer"), knownvalue.StringExact("https://oidc.subako.test")),
				statecheck.ExpectKnownValue(address, tfjsonpath.New("subject"), knownvalue.StringExact("workspace:"+api.workspaceID)),
				statecheck.ExpectKnownValue(address, tfjsonpath.New("aws").AtMapKey("audience"), knownvalue.StringExact("sts.amazonaws.com")),
				statecheck.ExpectKnownValue(address, tfjsonpath.New("aws").AtMapKey("issuer_key"), knownvalue.StringExact("oidc.subako.test")),
				statecheck.ExpectKnownValue(address, tfjsonpath.New("anthropic").AtMapKey("audience"),
					knownvalue.StringExact("https://api.anthropic.com")),
				statecheck.ExpectKnownValue(address, tfjsonpath.New("openai").AtMapKey("audience"),
					knownvalue.StringExact("https://api.openai.com/v1")),
			},
		}},
	})
}
