package provider

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// oauthClientHCL is a client authenticating with auth, under the given name,
// endpoint, and secret version.
func oauthClientHCL(name, authorizationEndpoint, auth string, version int) string {
	return fmt.Sprintf(`
resource "subako_oauth_client" "c" {
  display_name           = %q
  authorization_endpoint = %q
  token_endpoint         = "https://oauth.example.test/token"
  client_id              = "client-1"
  token_endpoint_auth    = %s
  client_secret_wo_version = %d

  scopes = ["drive.readonly", "openid"]
  authorization_params = [
    { name = "access_type", value = "offline" },
    { name = "prompt", value = "consent" },
  ]
}
`, name, authorizationEndpoint, auth, version)
}

const basicAuthHCL = `{ client_secret_basic = { client_secret_wo = "s3cret" } }`

// checkOAuthSecrets compares every token endpoint auth the client was sent,
// in order.
func checkOAuthSecrets(api *fakeAPI, want ...string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		id := s.RootModule().Resources["subako_oauth_client.c"].Primary.ID
		api.mu.Lock()
		defer api.mu.Unlock()
		var got []string
		for _, auth := range api.oauthSecrets[id] {
			got = append(got, fmt.Sprintf("%s:%v", auth["method"], auth["client_secret"]))
		}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			return fmt.Errorf("the client was sent %v, want %v", got, want)
		}
		return nil
	}
}

// The secret travels on create, when the method changes, and when its
// version is bumped -- never otherwise, and never into the state.
func TestAnOAuthClientsSecretTravelsOnlyWhenItMustAndNeverReachesTheState(t *testing.T) {
	api, server := newFakeAPI(t)
	head := providerBlock(server.URL, api.workspaceID)
	const endpoint = "https://oauth.example.test/authorize"

	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   versionChecks,
		ProtoV6ProviderFactories: factories,
		CheckDestroy:             checkEmpty(api),
		Steps: []resource.TestStep{
			{
				Config: head + oauthClientHCL("Drive", endpoint, basicAuthHCL, 1),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("subako_oauth_client.c",
						tfjsonpath.New("token_endpoint_auth").AtMapKey("client_secret_basic").AtMapKey("client_secret_wo"),
						knownvalue.Null()),
					statecheck.ExpectKnownValue("subako_oauth_client.c", tfjsonpath.New("workspace_id"),
						knownvalue.StringExact(api.workspaceID)),
					statecheck.ExpectKnownValue("subako_oauth_client.c",
						tfjsonpath.New("authorization_params").AtSliceIndex(1).AtMapKey("value"), knownvalue.StringExact("consent")),
				},
				Check: checkOAuthSecrets(api, "client_secret_basic:s3cret"),
			},
			{
				// A rename sends no secret.
				Config: head + oauthClientHCL("Google Drive", endpoint, basicAuthHCL, 1),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("subako_oauth_client.c", plancheck.ResourceActionUpdate)},
				},
				Check: resource.ComposeAggregateTestCheckFunc(
					checkOAuthSecrets(api, "client_secret_basic:s3cret"),
					resource.TestCheckResourceAttr("subako_oauth_client.c", "display_name", "Google Drive"),
				),
			},
			{
				Config: head + oauthClientHCL("Google Drive", endpoint, `{ client_secret_basic = { client_secret_wo = "rotated" } }`, 2),
				Check:  checkOAuthSecrets(api, "client_secret_basic:s3cret", "client_secret_basic:rotated"),
			},
			{
				Config: head + oauthClientHCL("Google Drive", endpoint, `{ client_secret_post = { client_secret_wo = "rotated" } }`, 2),
				Check: checkOAuthSecrets(api, "client_secret_basic:s3cret", "client_secret_basic:rotated",
					"client_secret_post:rotated"),
			},
			{
				Config: head + oauthClientHCL("Google Drive", endpoint, `{ none = {} }`, 2),
				Check: checkOAuthSecrets(api, "client_secret_basic:s3cret", "client_secret_basic:rotated",
					"client_secret_post:rotated", "none:<nil>"),
			},
			{
				ResourceName:            "subako_oauth_client.c",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"client_secret_wo_version"},
			},
		},
	})
}

// The server fixes a client's endpoints and client id, and stores an endpoint
// in its own spelling, which names the same endpoint as the configuration's.
func TestAnOAuthClientsEndpointsReplaceItAndReadBackInAnySpelling(t *testing.T) {
	api, server := newFakeAPI(t)
	head := providerBlock(server.URL, api.workspaceID)
	spelled := head + oauthClientHCL("Drive", "https://OAuth.Example.test", basicAuthHCL, 1)

	resource.UnitTest(t, resource.TestCase{
		TerraformVersionChecks:   versionChecks,
		ProtoV6ProviderFactories: factories,
		CheckDestroy:             checkEmpty(api),
		Steps: []resource.TestStep{
			{
				Config: spelled,
				Check: func(s *terraform.State) error {
					id := s.RootModule().Resources["subako_oauth_client.c"].Primary.ID
					api.mu.Lock()
					defer api.mu.Unlock()
					if got := api.oauthClients[id]["authorization_endpoint"]; got != "https://oauth.example.test/" {
						return fmt.Errorf("the server holds %v", got)
					}
					return nil
				},
			},
			{Config: spelled, PlanOnly: true},
			{
				Config: head + oauthClientHCL("Drive", "https://oauth.example.test/v2/authorize", basicAuthHCL, 1),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectResourceAction("subako_oauth_client.c", plancheck.ResourceActionReplace)},
				},
			},
		},
	})
}

func TestInvalidOAuthClientsFailValidation(t *testing.T) {
	api, server := newFakeAPI(t)
	head := providerBlock(server.URL, api.workspaceID)
	const endpoint = "https://oauth.example.test/authorize"
	cases := map[string]struct {
		config string
		err    string
	}{
		"two methods": {oauthClientHCL("c", endpoint,
			`{ none = {}, client_secret_post = { client_secret_wo = "s" } }`, 1), `Invalid Attribute Combination`},
		"no method": {oauthClientHCL("c", endpoint, `{}`, 1), `Missing Attribute Configuration`},
		"a secret without its version": {`
resource "subako_oauth_client" "c" {
  display_name           = "c"
  authorization_endpoint = "https://oauth.example.test/authorize"
  token_endpoint         = "https://oauth.example.test/token"
  client_id              = "client-1"
  token_endpoint_auth    = { client_secret_post = { client_secret_wo = "s" } }
}`, `Missing secret version`},
		"a parameter Subako sets itself": {`
resource "subako_oauth_client" "c" {
  display_name           = "c"
  authorization_endpoint = "https://oauth.example.test/authorize"
  token_endpoint         = "https://oauth.example.test/token"
  client_id              = "client-1"
  token_endpoint_auth    = { none = {} }
  authorization_params   = [{ name = "redirect_uri", value = "https://evil.example.test" }]
}`, `redirect_uri`},
		"a scope with a space": {`
resource "subako_oauth_client" "c" {
  display_name           = "c"
  authorization_endpoint = "https://oauth.example.test/authorize"
  token_endpoint         = "https://oauth.example.test/token"
  client_id              = "client-1"
  token_endpoint_auth    = { none = {} }
  scopes                 = ["drive readonly"]
}`, `printable ASCII`},
		"a scope listed twice": {`
resource "subako_oauth_client" "c" {
  display_name           = "c"
  authorization_endpoint = "https://oauth.example.test/authorize"
  token_endpoint         = "https://oauth.example.test/token"
  client_id              = "client-1"
  token_endpoint_auth    = { none = {} }
  scopes                 = ["openid", "openid"]
}`, `duplicate values`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				TerraformVersionChecks:   versionChecks,
				ProtoV6ProviderFactories: factories,
				Steps: []resource.TestStep{{
					Config:      head + tc.config,
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(tc.err),
				}},
			})
		})
	}
}

func TestWhatNamesTheSameEndpoint(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		same bool
	}{
		{"https://OAuth.Example.test", "https://oauth.example.test/", true},
		{"https://oauth.example.test:443/authorize", "https://oauth.example.test/authorize", true},
		{"http://oauth.example.test:80/", "http://oauth.example.test/", true},
		{"https://oauth.example.test:8443/", "https://oauth.example.test/", false},
		{"https://oauth.example.test/authorize?prompt=consent", "https://oauth.example.test/authorize", false},
		{"https://oauth.example.test/Authorize", "https://oauth.example.test/authorize", false},
		{"https://oauth.example.test/", "http://oauth.example.test/", false},
	} {
		if got := sameEndpoint(tc.a, tc.b); got != tc.same {
			t.Errorf("sameEndpoint(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.same)
		}
	}
}
