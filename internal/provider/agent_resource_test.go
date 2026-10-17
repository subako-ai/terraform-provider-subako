package provider

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/subako-ai/terraform-provider-subako/internal/client"
)

// anAgent is an agent holding one version's config, granting nothing.
func anAgent(ctx context.Context, t *testing.T) agentModel {
	t.Helper()
	var m agentModel
	prompt := "be brief"
	diags := setAgentConfig(ctx, &m, client.AgentConfig{
		ModelProviderID: platformProviderID,
		SystemPrompt:    &prompt,
		Model:           client.AnthropicModel{Model: "claude-sonnet-5", MaxTokens: 8192, ContextWindow: 1000000},
	}.Canonical())
	if diags.HasError() {
		t.Fatalf("building the agent: %v", diags)
	}
	return m
}

func TestWhatCountsAsAConfigChange(t *testing.T) {
	ctx := context.Background()
	state := anAgent(ctx, t)

	same := anAgent(ctx, t)

	unknown := anAgent(ctx, t)
	unknown.ModelProviderID = types.StringUnknown()

	another := anAgent(ctx, t)
	another.SystemPrompt = types.StringValue("be thorough")

	sandboxed := anAgent(ctx, t)
	sandboxed.Sandboxes = types.ListValueMust(sandboxGrantType, []attr.Value{
		types.ObjectValueMust(sandboxGrantType.AttrTypes, map[string]attr.Value{
			"name": types.StringValue("build"),
			"environment": types.ObjectValueMust(sandboxGrantType.AttrTypes["environment"].(types.ObjectType).AttrTypes,
				map[string]attr.Value{"platform": types.ObjectValueMust(
					map[string]attr.Type{"release": types.StringType},
					map[string]attr.Value{"release": types.StringValue("2026")})}),
			"network":        types.StringValue(client.SandboxNetworkNone),
			"default_policy": types.StringValue("allow"),
			"tools":          types.ListValueMust(toolRuleType, []attr.Value{}),
		}),
	})

	// An agent whose versions were all published elsewhere, or none at all.
	var versionless agentModel
	clearAgentConfig(&versionless)

	for name, tc := range map[string]struct {
		plan, state agentModel
		changed     bool
	}{
		"the same config":                {same, state, false},
		"another system prompt":          {another, state, true},
		"a sandbox granted":              {sandboxed, state, true},
		"a value known only after apply": {unknown, state, true},
		"an agent holding no version":    {state, versionless, true},
	} {
		t.Run(name, func(t *testing.T) {
			changed, diags := configChanged(ctx, tc.plan, tc.state)
			if diags.HasError() {
				t.Fatalf("comparing: %v", diags)
			}
			if changed != tc.changed {
				t.Fatalf("changed = %v, want %v", changed, tc.changed)
			}
		})
	}
}

// The defaults and the read-back build values of the types written out in
// agent_schema.go, so those must be the types the schema gives.
func TestTheWrittenOutTypesAreTheSchemas(t *testing.T) {
	schema := agentSchema()
	for name, want := range map[string]attr.Type{
		"mcp_servers": types.ListType{ElemType: mcpServerType},
		"skills":      types.ListType{ElemType: skillGrantType},
		"sandboxes":   types.ListType{ElemType: sandboxGrantType},
		"functions":   types.ListType{ElemType: functionGrantType},
		"volume":      volumeType,
		"builtin":     builtinType,
	} {
		if got := schema.Attributes[name].GetType(); !got.Equal(want) {
			t.Errorf("%s: the schema gives %s, the code writes %s", name, got, want)
		}
	}
}

// A version 0 state upgrades to one the current schema decodes, its thinking
// budget carried over as enabled thinking.
func TestAVersion0StateUpgrades(t *testing.T) {
	v0 := `{
		"id": "a", "workspace_id": "w", "name": "support", "model_provider_id": "mp", "system_prompt": null,
		"model": {
			"anthropic": {"name": "claude", "max_tokens": 8, "context_window": 16, "thinking_budget_tokens": 4},
			"openai_responses": null
		},
		"mcp_servers": [{"name": "gh", "url": "https://mcp", "default_policy": "allow", "tools": null}],
		"skills": null, "allowed_origins": ["*"], "version": 1, "version_id": "v"
	}`
	upgraded, err := upgradeAgentJSONV0([]byte(v0))
	if err != nil {
		t.Fatal(err)
	}
	stateType := agentSchema().Type().TerraformType(context.Background())
	if _, err := tftypes.ValueFromJSONWithOpts(upgraded, stateType, tftypes.ValueFromJSONOpts{}); err != nil {
		t.Fatalf("the upgraded state does not decode: %v\n%s", err, upgraded)
	}
	var state map[string]any
	_ = json.Unmarshal(upgraded, &state)
	thinking := state["model"].(map[string]any)["anthropic"].(map[string]any)["thinking"].(map[string]any)
	if budget := thinking["enabled"].(map[string]any)["budget_tokens"]; budget != 4.0 {
		t.Fatalf("thinking = %v", thinking)
	}

	versionless := `{
		"id": "a", "workspace_id": "w", "name": "support", "model_provider_id": null, "system_prompt": null,
		"model": null, "mcp_servers": null, "skills": null, "allowed_origins": [], "version": null, "version_id": null
	}`
	upgraded, err = upgradeAgentJSONV0([]byte(versionless))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tftypes.ValueFromJSONWithOpts(upgraded, stateType, tftypes.ValueFromJSONOpts{}); err != nil {
		t.Fatalf("the upgraded versionless state does not decode: %v\n%s", err, upgraded)
	}
}
