package client

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// Each auth goes in as `ModelProviderAuthInput` spells it, and comes back as
// `ModelProviderAuthBody` does: the same fields, but never a key.
func TestEveryModelProviderAuthKeepsItsWireShape(t *testing.T) {
	cases := []struct {
		auth ModelProviderAuth
		sent string
		read ModelProviderAuth
	}{
		{
			auth: APIKeyAuth{APIKey: "sk-1"},
			sent: `{"api_key":"sk-1","type":"api_key"}`,
			read: APIKeyAuth{},
		},
		{
			auth: AWSWebIdentityAuth{RoleARN: "arn:aws:iam::123456789012:role/r", Region: "us-east-1"},
			sent: `{"region":"us-east-1","role_arn":"arn:aws:iam::123456789012:role/r","type":"aws_web_identity"}`,
		},
		{
			auth: AnthropicWorkloadIdentityAuth{OrganizationID: "org", ServiceAccountID: "svac_1", FederationRuleID: "fdrl_1"},
			sent: `{"federation_rule_id":"fdrl_1","organization_id":"org","service_account_id":"svac_1","type":"anthropic_workload_identity"}`,
		},
		{
			auth: AnthropicWorkloadIdentityAuth{OrganizationID: "org", ServiceAccountID: "svac_1", FederationRuleID: "fdrl_1",
				WorkspaceID: strp("wrkspc_1")},
			sent: `{"federation_rule_id":"fdrl_1","organization_id":"org","service_account_id":"svac_1","type":"anthropic_workload_identity","workspace_id":"wrkspc_1"}`,
		},
		{
			auth: OpenAIWorkloadIdentityAuth{IdentityProviderID: "idp_1", ServiceAccountID: "user-1"},
			sent: `{"identity_provider_id":"idp_1","service_account_id":"user-1","type":"openai_workload_identity"}`,
		},
	}
	for _, c := range cases {
		t.Run(c.auth.Type(), func(t *testing.T) {
			sent, err := json.Marshal(c.auth)
			if err != nil {
				t.Fatal(err)
			}
			if string(sent) != c.sent {
				t.Errorf("sent %s, want %s", sent, c.sent)
			}
			want := c.read
			if want == nil {
				want = c.auth
			}
			var provider ModelProvider
			body := `{"type":"byok","id":"p","format":"anthropic","display_name":"P","description":"","auth":` + c.sent + `}`
			if err := json.Unmarshal([]byte(body), &provider); err != nil {
				t.Fatal(err)
			}
			read, err := provider.AuthOf()
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(read, want) {
				t.Errorf("read back %#v, want %#v", read, want)
			}
		})
	}
}

// A listing reads past an auth this client does not know; only the read that
// asks for it fails.
func TestAnUnknownAuthFailsOnlyItsOwnRead(t *testing.T) {
	var provider ModelProvider
	body := `{"type":"byok","id":"p","format":"anthropic","display_name":"P","description":"","auth":{"type":"gcp"}}`
	if err := json.Unmarshal([]byte(body), &provider); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.AuthOf(); err == nil || !strings.Contains(err.Error(), `"gcp"`) {
		t.Errorf("err = %v", err)
	}
}
