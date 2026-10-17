package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"

	"github.com/subako-ai/terraform-provider-subako/internal/client"
)

// UpgradeState moves a state written by an earlier schema to the current
// one. The read that follows refreshes every config value from the server,
// so an upgrade only has to give each attribute the shape the current
// schema expects.
func (r *agentResource) UpgradeState(context.Context) map[int64]resource.StateUpgrader {
	return map[int64]resource.StateUpgrader{
		0: {StateUpgrader: upgradeAgentStateV0},
	}
}

// upgradeAgentStateV0 upgrades a state from before the schema mirrored the
// whole config: an anthropic model's thinking was a budget alone, and the
// grants beyond MCP servers and skills had no attributes.
func upgradeAgentStateV0(_ context.Context, req resource.UpgradeStateRequest, resp *resource.UpgradeStateResponse) {
	upgraded, err := upgradeAgentJSONV0(req.RawState.JSON)
	if err != nil {
		resp.Diagnostics.AddError("Unreadable agent state", err.Error())
		return
	}
	resp.DynamicValue = &tfprotov6.DynamicValue{JSON: upgraded}
}

func upgradeAgentJSONV0(raw []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var state map[string]any
	if err := decoder.Decode(&state); err != nil {
		return nil, fmt.Errorf("decode version 0 state: %w", err)
	}

	model, versioned := state["model"].(map[string]any)
	if !versioned {
		for _, name := range []string{"sandboxes", "functions", "volume", "builtin"} {
			state[name] = nil
		}
		return json.Marshal(state)
	}

	if anthropic, ok := model[client.FormatAnthropic].(map[string]any); ok {
		var thinking any
		if budget := anthropic["thinking_budget_tokens"]; budget != nil {
			thinking = map[string]any{
				client.ThinkingAdaptive:     nil,
				client.ThinkingDisabled:     nil,
				client.ThinkingBetweenTools: nil,
				client.ThinkingEnabled:      map[string]any{"budget_tokens": budget},
			}
		}
		delete(anthropic, "thinking_budget_tokens")
		anthropic["thinking"] = thinking
		anthropic["effort"] = nil
		anthropic["image_input"] = client.ImageInputSupported
	}
	if openai, ok := model[client.FormatOpenAIResponses].(map[string]any); ok {
		openai["image_input"] = client.ImageInputSupported
	}

	servers, _ := state["mcp_servers"].([]any)
	for _, server := range servers {
		if grant, ok := server.(map[string]any); ok && grant["tools"] == nil {
			grant["tools"] = []any{}
		}
	}
	for _, name := range []string{"mcp_servers", "skills"} {
		if state[name] == nil {
			state[name] = []any{}
		}
	}
	state["sandboxes"] = []any{}
	state["functions"] = []any{}
	state["volume"] = map[string]any{"data": client.VolumeDataDisabled}
	state["builtin"] = map[string]any{"session_display_name": client.DisplayNameNever, "ask_user": nil}
	return json.Marshal(state)
}
