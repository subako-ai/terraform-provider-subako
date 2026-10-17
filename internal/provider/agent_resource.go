package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/subako-ai/terraform-provider-subako/internal/client"
)

var (
	_ resource.ResourceWithConfigure    = (*agentResource)(nil)
	_ resource.ResourceWithImportState  = (*agentResource)(nil)
	_ resource.ResourceWithModifyPlan   = (*agentResource)(nil)
	_ resource.ResourceWithUpgradeState = (*agentResource)(nil)
)

func newAgentResource() resource.Resource {
	return &agentResource{}
}

// agentResource is an agent and the config its latest version carries.
// Changing the config publishes a new version; versions are immutable and
// are never removed on their own, so the agent is the unit Terraform owns.
type agentResource struct {
	client *client.Client
}

type agentModel struct {
	ID              types.String `tfsdk:"id"`
	WorkspaceID     types.String `tfsdk:"workspace_id"`
	Name            types.String `tfsdk:"name"`
	ModelProviderID types.String `tfsdk:"model_provider_id"`
	SystemPrompt    types.String `tfsdk:"system_prompt"`
	Model           types.Object `tfsdk:"model"`
	MCPServers      types.List   `tfsdk:"mcp_servers"`
	Skills          types.List   `tfsdk:"skills"`
	Sandboxes       types.List   `tfsdk:"sandboxes"`
	Functions       types.List   `tfsdk:"functions"`
	Volume          types.Object `tfsdk:"volume"`
	Builtin         types.Object `tfsdk:"builtin"`
	AllowedOrigins  types.Set    `tfsdk:"allowed_origins"`
	Version         types.Int64  `tfsdk:"version"`
	VersionID       types.String `tfsdk:"version_id"`
}

func (r *agentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_agent"
}

func (r *agentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = agentSchema()
}

func (r *agentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

// configChanged reports whether an apply would publish a new version: the
// config the plan sends differs from the one the agent's latest version
// carries. The two are compared as the configs they become. A plan value
// known only after apply could be anything, so it counts as a change.
func configChanged(ctx context.Context, plan, state agentModel) (bool, diag.Diagnostics) {
	known, diags := fullyKnown(ctx, plan.configValues()...)
	if diags.HasError() || !known {
		return true, diags
	}
	// An agent holding no version carries no config to compare against.
	if _, ok := planned(state.Model).Get(); !ok {
		return true, diags
	}
	wanted, d := agentConfig(ctx, plan)
	diags.Append(d...)
	held, d := agentConfig(ctx, state)
	diags.Append(d...)
	if diags.HasError() {
		return true, diags
	}
	return !wanted.Equal(held), diags
}

// ModifyPlan keeps the version a rename-only change leaves in place, so the
// plan says "known after apply" only when a new version will be published.
func (r *agentResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var plan, state agentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	changed, diags := configChanged(ctx, plan, state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() || changed {
		return
	}
	plan.Version = state.Version
	plan.VersionID = state.VersionID
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

// read refreshes m from the server. found is false when the agent is gone.
func (r *agentResource) read(ctx context.Context, m *agentModel) (found bool, diags diag.Diagnostics) {
	id := m.ID.ValueString()
	agent, err := r.client.GetAgent(ctx, id)
	if client.IsNotFound(err) {
		return false, diags
	}
	if err != nil {
		apiError(&diags, "read agent "+id, err)
		return true, diags
	}
	m.WorkspaceID = types.StringValue(agent.WorkspaceID)
	m.Name = types.StringValue(agent.Name)

	version, err := r.client.LatestAgentVersion(ctx, id)
	if err != nil {
		apiError(&diags, "read agent "+id+" versions", err)
		return true, diags
	}
	if version == nil {
		clearAgentConfig(m)
		m.Version = types.Int64Null()
		m.VersionID = types.StringNull()
	} else {
		config, err := version.AgentConfig()
		if err != nil {
			diags.AddError("Unreadable agent version", err.Error())
			return true, diags
		}
		diags.Append(setAgentConfig(ctx, m, config)...)
		m.Version = types.Int64Value(version.Version)
		m.VersionID = types.StringValue(version.ID)
	}

	security, err := r.client.GetAgentSecurity(ctx, id)
	if err != nil {
		apiError(&diags, "read agent "+id+" security", err)
		return true, diags
	}
	origins, d := types.SetValueFrom(ctx, types.StringType, security.AllowedOrigins)
	diags.Append(d...)
	m.AllowedOrigins = origins
	return true, diags
}

// putOrigins replaces an agent's browser-origin allowlist.
func (r *agentResource) putOrigins(ctx context.Context, id string, origins []string) step {
	return step{
		action: "set agent " + id + " allowed origins",
		call: func() error {
			_, err := r.client.PutAgentSecurity(ctx, id, client.AgentSecurity{AllowedOrigins: origins})
			return err
		},
	}
}

func (r *agentResource) publish(ctx context.Context, id string, config client.AgentConfig) step {
	return step{
		action: "publish agent " + id + " version",
		call: func() error {
			_, err := r.client.PublishAgentVersion(ctx, id, config)
			return err
		},
	}
}

func (r *agentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan agentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	config, diags := agentConfig(ctx, plan)
	resp.Diagnostics.Append(diags...)
	allowed, diags := plannedStrings(ctx, plan.AllowedOrigins)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	agent, err := r.client.CreateAgent(ctx, plan.Name.ValueString())
	if err != nil {
		apiError(&resp.Diagnostics, "create agent", err)
		return
	}
	// Recorded before the steps below, so a failure there taints this agent
	// rather than orphaning it.
	created := plan
	created.ID = types.StringValue(agent.ID)
	created.WorkspaceID = types.StringValue(agent.WorkspaceID)
	created.Name = types.StringValue(agent.Name)
	created.AllowedOrigins = types.SetNull(types.StringType)
	created.Version = types.Int64Null()
	created.VersionID = types.StringNull()

	steps := []step{r.publish(ctx, agent.ID, config)}
	if origins, ok := allowed.Get(); ok {
		steps = append(steps, r.putOrigins(ctx, agent.ID, origins))
	}
	run(&resp.Diagnostics, steps...)

	next := plan
	next.ID = created.ID
	finishCreate(ctx, resp, subject{"Agent", agent.ID}, r.read, next, created)
}

func (r *agentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	refresh(ctx, req, resp, r.read)
}

func (r *agentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state agentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()

	var steps []step
	if !plan.Name.Equal(state.Name) {
		steps = append(steps, step{
			action: "rename agent " + id,
			call: func() error {
				_, err := r.client.RenameAgent(ctx, id, plan.Name.ValueString())
				return err
			},
		})
	}
	changed, diags := configChanged(ctx, plan, state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if changed {
		config, d := agentConfig(ctx, plan)
		resp.Diagnostics.Append(d...)
		if resp.Diagnostics.HasError() {
			return
		}
		steps = append(steps, r.publish(ctx, id, config))
	}
	wanted, d := plannedStrings(ctx, plan.AllowedOrigins)
	resp.Diagnostics.Append(d...)
	held, d := plannedStrings(ctx, state.AllowedOrigins)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	if origins, ok := wanted.Get(); ok && !wanted.Equal(held) {
		steps = append(steps, r.putOrigins(ctx, id, origins))
	}
	run(&resp.Diagnostics, steps...)

	next := plan
	next.ID = state.ID
	finishUpdate(ctx, resp, subject{"Agent", id}, r.read, next, state)
}

func (r *agentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state agentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	remove(resp, "delete agent "+id, func() error { return r.client.DeleteAgent(ctx, id) })
}

func (r *agentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
