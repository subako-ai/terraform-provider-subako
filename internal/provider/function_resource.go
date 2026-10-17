package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/subako-ai/terraform-provider-subako/internal/bundle"
	"github.com/subako-ai/terraform-provider-subako/internal/client"
)

var (
	_ resource.ResourceWithConfigure   = (*functionResource)(nil)
	_ resource.ResourceWithImportState = (*functionResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*functionResource)(nil)
)

func newFunctionResource() resource.Resource {
	return &functionResource{}
}

// functionResource is a function package whose latest version holds a local
// bundle directory's content. A change to the manifest, the module, or any
// asset pushes a new version; versions are immutable, so the package is the
// unit Terraform owns.
type functionResource struct {
	client *client.Client
}

type functionModel struct {
	ID            types.String `tfsdk:"id"`
	SourceDir     types.String `tfsdk:"source_dir"`
	DisplayName   types.String `tfsdk:"display_name"`
	Name          types.String `tfsdk:"name"`
	ContentHash   types.String `tfsdk:"content_hash"`
	LatestVersion types.Int64  `tfsdk:"latest_version"`
}

func (r *functionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_function"
}

func (r *functionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A function package uploaded from a built bundle directory. Changing the manifest, " +
			"the module, or any asset pushes a new version.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"source_dir": schema.StringAttribute{
				Required: true,
				Description: "The bundle directory: `subako-package.json` and `module.wasm` at its root, " +
					"and the files the module reads under `assets/`. Nothing else may sit in it.",
			},
			"display_name": schema.StringAttribute{
				Optional:    true,
				Computed:    true,
				Description: "A label shown in place of the package name. Left unset, Terraform does not manage it.",
				// CODESYNC(display-name-len)
				Validators:    []validator.String{charsAtMost(200)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Computed:    true,
				Description: "The package `name` the latest version's manifest states.",
			},
			"content_hash": schema.StringAttribute{
				Computed: true,
				Description: "A fingerprint of the latest version's manifest, module, and assets. Terraform " +
					"compares it with the directory's to decide whether to push.",
			},
			"latest_version": schema.Int64Attribute{
				Computed: true,
			},
		},
	}
}

func (r *functionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.client = clientFrom(req.ProviderData, &resp.Diagnostics)
}

// readFunctionBundle reads the directory source_dir names, raising what was
// wrong with it against that attribute.
func readFunctionBundle(dir string, diags *diag.Diagnostics) *bundle.Function {
	local, err := bundle.ReadFunction(dir)
	if err != nil {
		diags.AddAttributeError(path.Root("source_dir"), "Unreadable function bundle", err.Error())
		return nil
	}
	return local
}

// ModifyPlan fingerprints the directory, so a change to its files is a
// change to the plan. An unchanged fingerprint keeps what the version
// reported; a changed one leaves it to the push.
func (r *functionResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var plan functionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || plan.SourceDir.IsUnknown() {
		return
	}
	local := readFunctionBundle(plan.SourceDir.ValueString(), &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	plan.ContentHash = types.StringValue(local.Hash())
	plan.Name = types.StringUnknown()
	plan.LatestVersion = types.Int64Unknown()

	if !req.State.Raw.IsNull() {
		var state functionModel
		resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if plan.ContentHash.Equal(state.ContentHash) {
			plan.Name = state.Name
			plan.LatestVersion = state.LatestVersion
		}
	}
	resp.Diagnostics.Append(resp.Plan.Set(ctx, &plan)...)
}

func (r *functionResource) read(ctx context.Context, m *functionModel) (found bool, diags diag.Diagnostics) {
	id := m.ID.ValueString()
	function, err := r.client.GetFunction(ctx, id)
	if client.IsNotFound(err) {
		return false, diags
	}
	if err != nil {
		apiError(&diags, "read function "+id, err)
		return true, diags
	}
	version, err := r.client.LatestFunctionVersion(ctx, id)
	if err != nil {
		apiError(&diags, "read function "+id+" versions", err)
		return true, diags
	}
	m.DisplayName = types.StringValue(function.DisplayName)
	m.Name = types.StringValue(function.Name)
	m.LatestVersion = types.Int64Value(function.LatestVersion)
	m.ContentHash = types.StringNull()
	if version != nil {
		var assets []bundle.Entry
		if version.Assets != nil {
			for _, file := range version.Assets.Files {
				assets = append(assets, bundle.Entry{Path: file.Path, Sha256: file.Sha256})
			}
		}
		m.ContentHash = types.StringValue(bundle.FunctionHash(
			version.Manifest.Name, version.Manifest.Description, version.ModuleSha256, assets))
	}
	return true, diags
}

func packFunction(dir string, diags *diag.Diagnostics) []byte {
	local := readFunctionBundle(dir, diags)
	if diags.HasError() {
		return nil
	}
	archive, err := local.Pack()
	if err != nil {
		diags.AddAttributeError(path.Root("source_dir"), "Unpackable function bundle", err.Error())
		return nil
	}
	return archive
}

// rename gives a function package its display name.
func (r *functionResource) rename(ctx context.Context, id, displayName string) step {
	return step{
		action: "name function " + id,
		call: func() error {
			_, err := r.client.RenameFunction(ctx, id, displayName)
			return err
		},
	}
}

func (r *functionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan functionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	archive := packFunction(plan.SourceDir.ValueString(), &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	version, err := r.client.CreateFunction(ctx, archive)
	if err != nil {
		apiError(&resp.Diagnostics, "create function", err)
		return
	}
	// Recorded before the steps below, so a failure there taints this
	// package rather than orphaning it.
	created := functionModel{
		ID:            types.StringValue(version.FunctionID),
		SourceDir:     plan.SourceDir,
		DisplayName:   types.StringNull(),
		Name:          types.StringNull(),
		ContentHash:   types.StringNull(),
		LatestVersion: types.Int64Null(),
	}

	if displayName, ok := plannedString(plan.DisplayName).Get(); ok {
		run(&resp.Diagnostics, r.rename(ctx, version.FunctionID, displayName))
	}

	next := plan
	next.ID = created.ID
	finishCreate(ctx, resp, subject{"Function", version.FunctionID}, r.read, next, created)
}

func (r *functionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	refresh(ctx, req, resp, r.read)
}

func (r *functionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state functionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()

	var steps []step
	if !plan.ContentHash.Equal(state.ContentHash) {
		archive := packFunction(plan.SourceDir.ValueString(), &resp.Diagnostics)
		if resp.Diagnostics.HasError() {
			return
		}
		steps = append(steps, step{
			action: "push function " + id + " version",
			call: func() error {
				_, err := r.client.PushFunctionVersion(ctx, id, archive)
				return err
			},
		})
	}
	wanted := plannedString(plan.DisplayName)
	if displayName, ok := wanted.Get(); ok && !wanted.Equal(plannedString(state.DisplayName)) {
		steps = append(steps, r.rename(ctx, id, displayName))
	}
	run(&resp.Diagnostics, steps...)

	next := plan
	next.ID = state.ID
	finishUpdate(ctx, resp, subject{"Function", id}, r.read, next, state)
}

func (r *functionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state functionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	remove(resp, "delete function "+id+
		" (a function cannot be deleted while any published agent version or live session grants it)",
		func() error { return r.client.DeleteFunction(ctx, id) })
}

func (r *functionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
