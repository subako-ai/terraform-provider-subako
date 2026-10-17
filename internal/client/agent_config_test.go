package client

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func int64p(n int64) *int64 { return &n }
func strp(s string) *string { return &s }

// versionConfig is a config as a version answers it: every field written
// out, as the server serializes `AgentConfigBody`.
const versionConfig = `{
	"system_prompt": "be brief",
	"model": {
		"format": "anthropic", "model": "claude-sonnet-5", "max_tokens": 8192, "context_window": 1000000,
		"image_input": "unsupported", "thinking": {"type": "enabled", "budget_tokens": 1024}, "effort": "high"
	},
	"mcp": [{"name": "gh", "url": "https://mcp", "default_policy": "require_approval", "tools": [{"name": "t", "policy": "deny"}]}],
	"skills": [{"name": "docs", "skill_id": "sk", "version": {"type": "pinned", "number": 2}}],
	"sandboxes": [{
		"name": "build", "environment": {"type": "platform", "release": "2026"}, "network": {"type": "none"},
		"default_policy": "allow", "tools": []
	}],
	"functions": [
		{
			"name": "files", "source": {"type": "platform", "package": "files"}, "default_policy": "allow",
			"tools": [], "timeout_secs": null, "network": {"type": "none"}, "http_credentials": []
		},
		{
			"name": "gh", "source": {"type": "workspace", "function_id": "fn", "version": {"type": "latest"}},
			"default_policy": "deny", "tools": [{"name": "merge", "policy": "require_approval"}], "timeout_secs": 120,
			"network": {"type": "allowlist", "hosts": ["api.github.com"]}, "http_credentials": ["https://api.github.com"]
		}
	],
	"volume": {"data": {"type": "enabled"}},
	"builtin": {
		"session_display_name": {"when": "until_user_edits"},
		"ask_user": {"widgets": [
			{"type": "select"},
			{"type": "custom", "id": "date", "description": "Pick a date.", "props": {"type": "object"}, "answer": {"type": "string", "format": "date"}}
		]}
	}
}`

// fullConfig is versionConfig as the provider holds it.
func fullConfig() AgentConfig {
	return AgentConfig{
		ModelProviderID: "mp",
		SystemPrompt:    strp("be brief"),
		Model: AnthropicModel{
			Model: "claude-sonnet-5", MaxTokens: 8192, ContextWindow: 1000000,
			ImageInput: ImageInputUnsupported, Thinking: EnabledThinking{BudgetTokens: 1024}, Effort: strp("high"),
		},
		MCP: []MCPGrant{{Name: "gh", URL: "https://mcp", DefaultPolicy: "require_approval",
			Tools: []ToolRule{{Name: "t", Policy: "deny"}}}},
		Skills: []SkillGrant{{Name: "docs", SkillID: "sk", Version: int64p(2)}},
		Sandboxes: []SandboxGrant{{Name: "build", Release: "2026", Network: SandboxNetworkNone,
			DefaultPolicy: "allow"}},
		Functions: []FunctionGrant{
			{Name: "files", Source: PlatformFunction{Package: "files"}, DefaultPolicy: "allow", Network: NoNetwork{}},
			{
				Name: "gh", Source: WorkspaceFunction{FunctionID: "fn"}, DefaultPolicy: "deny",
				Tools:       []ToolRule{{Name: "merge", Policy: "require_approval"}},
				TimeoutSecs: int64p(120), Network: AllowlistNetwork{Hosts: []string{"api.github.com"}},
				HTTPCredentials: []string{"https://api.github.com"},
			},
		},
		Volume: VolumeConfig{Data: VolumeDataEnabled},
		Builtin: BuiltinGrants{
			SessionDisplayName: DisplayNameUntilUserEdits,
			AskUser: &AskUserGrant{Widgets: []Widget{
				SelectWidget{},
				CustomWidget{ID: "date", Description: "Pick a date.",
					Props: `{"type":"object"}`, Answer: `{"format":"date","type":"string"}`},
			}},
		},
	}
}

func TestAVersionConfigReadsBackAsWhatWasPublished(t *testing.T) {
	version := AgentVersion{Version: 4, ModelProviderID: "mp", Config: json.RawMessage(versionConfig)}
	got, err := version.AgentConfig()
	if err != nil {
		t.Fatal(err)
	}
	if want := fullConfig(); !got.Equal(want) {
		t.Fatalf("\n got %+v\nwant %+v", got, want)
	}
}

// What the provider publishes is what a version answers, so a config
// published and read back is the config the provider started from.
func TestThePublishBodyIsTheShapeAVersionAnswers(t *testing.T) {
	body, err := fullConfig().publishBody()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var published map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &published); err != nil {
		t.Fatal(err)
	}
	if string(published["model_provider_id"]) != `"mp"` {
		t.Fatalf("model_provider_id = %s", published["model_provider_id"])
	}
	delete(published, "model_provider_id")
	config, _ := json.Marshal(published)

	if got, want := CanonicalJSON(string(config)), CanonicalJSON(versionConfig); got != want {
		t.Fatalf("published config\n got %s\nwant %s", got, want)
	}
}

// Every value left out is written as the server would read it, and an
// empty list as an empty list, which the server takes where it refuses null.
func TestAMinimalConfigPublishesEveryDefault(t *testing.T) {
	body, err := AgentConfig{ModelProviderID: "mp", Model: OpenAIResponsesModel{Model: "gpt", MaxTokens: 1, ContextWindow: 2}}.publishBody()
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(body)
	want := `{"builtin":{"ask_user":null,"session_display_name":{"when":"never"}},"functions":[],"mcp":[],` +
		`"model":{"context_window":2,"format":"openai_responses","image_input":"supported","max_tokens":1,"model":"gpt","reasoning_effort":null},` +
		`"model_provider_id":"mp","sandboxes":[],"skills":[],"system_prompt":null,"volume":{"data":{"type":"disabled"}}}`
	if got := CanonicalJSON(string(encoded)); got != want {
		t.Fatalf("\n got %s\nwant %s", got, want)
	}
}

func TestCanonicalFoldsEmptyListsAndSpellsOutDefaults(t *testing.T) {
	folded := AgentConfig{
		Model:     AnthropicModel{Model: "claude"},
		MCP:       []MCPGrant{{Name: "x", Tools: []ToolRule{}}},
		Skills:    []SkillGrant{},
		Sandboxes: []SandboxGrant{{Name: "s", Tools: []ToolRule{}}},
		Functions: []FunctionGrant{{Name: "f", HTTPCredentials: []string{}}},
	}.Canonical()
	want := AgentConfig{
		Model:     AnthropicModel{Model: "claude", ImageInput: ImageInputSupported},
		MCP:       []MCPGrant{{Name: "x"}},
		Sandboxes: []SandboxGrant{{Name: "s", Network: SandboxNetworkUnrestricted}},
		Functions: []FunctionGrant{{Name: "f", Network: NoNetwork{}}},
		Volume:    VolumeConfig{Data: VolumeDataDisabled},
		Builtin:   BuiltinGrants{SessionDisplayName: DisplayNameNever},
	}
	if !reflect.DeepEqual(folded, want) {
		t.Fatalf("\n got %+v\nwant %+v", folded, want)
	}
}

func TestEquivalentWidgetSchemasCompareEqual(t *testing.T) {
	spelled := func(props string) AgentConfig {
		return AgentConfig{Model: AnthropicModel{Model: "m"}, Builtin: BuiltinGrants{AskUser: &AskUserGrant{
			Widgets: []Widget{CustomWidget{ID: "w", Props: props, Answer: "{}"}},
		}}}.Canonical()
	}
	if !spelled(`{ "b": 1, "a": [true] }`).Equal(spelled(`{"a":[true],"b":1}`)) {
		t.Fatal("the same schema spelled two ways compared unequal")
	}
	if spelled(`{"a":1}`).Equal(spelled(`{"a":2}`)) {
		t.Fatal("different schemas compared equal")
	}
}

// A field this provider has no type for would be dropped by the next
// publish, so reading one is an error wherever it sits.
func TestAVersionConfigHoldingSomethingUnknownIsRefused(t *testing.T) {
	cases := map[string]struct{ from, to string }{
		"a top-level field":         {`"system_prompt": "be brief"`, `"system_prompt": "be brief", "subagent": {}`},
		"a model setting":           {`"effort": "high"`, `"effort": "high", "speed": "fast"`},
		"a model format":            {`"format": "anthropic"`, `"format": "gemini"`},
		"a thinking kind":           {`"type": "enabled", "budget_tokens": 1024`, `"type": "eager"`},
		"a thinking setting":        {`"budget_tokens": 1024`, `"budget_tokens": 1024, "floor": 1`},
		"an effort":                 {`"effort": "high"`, `"effort": "ludicrous"`},
		"an image input":            {`"image_input": "unsupported"`, `"image_input": "video"`},
		"an mcp grant field":        {`"url": "https://mcp"`, `"url": "https://mcp", "headers": {}`},
		"a version pin":             {`"type": "pinned", "number": 2`, `"type": "range", "number": 2`},
		"an environment":            {`{"type": "platform", "release": "2026"}`, `{"type": "workspace", "release": "2026"}`},
		"a sandbox network":         {`"network": {"type": "none"}`, `"network": {"type": "vpn"}`},
		"a function source":         {`{"type": "platform", "package": "files"}`, `{"type": "registry", "package": "files"}`},
		"a function network field":  {`"hosts": ["api.github.com"]`, `"hosts": ["api.github.com"], "ports": [443]`},
		"a volume root":             {`"data": {"type": "enabled"}`, `"data": {"type": "enabled"}, "scratch": {"type": "enabled"}`},
		"a display name setting":    {`"when": "until_user_edits"`, `"when": "always"`},
		"a widget":                  {`{"type": "select"}`, `{"type": "slider"}`},
		"a custom widget field":     {`"description": "Pick a date."`, `"description": "Pick a date.", "icon": "cal"`},
		"a built-in server":         {`"session_display_name"`, `"memory": {}, "session_display_name"`},
		"a function grant field":    {`"timeout_secs": 120`, `"timeout_secs": 120, "memory_mb": 64`},
		"a sandbox grant field":     {`"default_policy": "allow", "tools": []`, `"default_policy": "allow", "tools": [], "cpus": 2`},
		"a widget kind with fields": {`{"type": "select"}`, `{"type": "select", "options": []}`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if !strings.Contains(versionConfig, tc.from) {
				t.Fatalf("the fixture holds no %s", tc.from)
			}
			edited := strings.Replace(versionConfig, tc.from, tc.to, 1)
			version := AgentVersion{Config: json.RawMessage(edited)}
			if _, err := version.AgentConfig(); err == nil {
				t.Fatalf("decoded %s", edited)
			}
		})
	}
}
