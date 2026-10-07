package provider

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/a2ito/terraform-provider-cloudflare/internal/client"
)

var (
	_ resource.Resource                   = (*workersBuildTriggerResource)(nil)
	_ resource.ResourceWithConfigure      = (*workersBuildTriggerResource)(nil)
	_ resource.ResourceWithImportState    = (*workersBuildTriggerResource)(nil)
	_ resource.ResourceWithValidateConfig = (*workersBuildTriggerResource)(nil)
)

type workersBuildTriggerResource struct {
	client *client.Client
}

type workersBuildTriggerModel struct {
	ID                         types.String `tfsdk:"id"`
	AccountID                  types.String `tfsdk:"account_id"`
	ScriptName                 types.String `tfsdk:"script_name"`
	ScriptTag                  types.String `tfsdk:"script_tag"`
	RepoConnectionUUID         types.String `tfsdk:"repo_connection_uuid"`
	BuildTokenUUID             types.String `tfsdk:"build_token_uuid"`
	TriggerName                types.String `tfsdk:"trigger_name"`
	BuildCommand               types.String `tfsdk:"build_command"`
	DeployCommand              types.String `tfsdk:"deploy_command"`
	RootDirectory              types.String `tfsdk:"root_directory"`
	BranchIncludes             types.Set    `tfsdk:"branch_includes"`
	BranchExcludes             types.Set    `tfsdk:"branch_excludes"`
	PathIncludes               types.Set    `tfsdk:"path_includes"`
	PathExcludes               types.Set    `tfsdk:"path_excludes"`
	BuildCachingEnabled        types.Bool   `tfsdk:"build_caching_enabled"`
	EnvironmentVariables       types.Map    `tfsdk:"environment_variables"`
	SecretEnvironmentVariables types.Map    `tfsdk:"secret_environment_variables"`
}

func NewWorkersBuildTriggerResource() resource.Resource {
	return &workersBuildTriggerResource{}
}

func (r *workersBuildTriggerResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_workers_build_trigger"
}

func emptyStringSet() types.Set {
	return types.SetValueMust(types.StringType, nil)
}

func (r *workersBuildTriggerResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	stringSet := func(desc string) schema.SetAttribute {
		return schema.SetAttribute{
			Description: desc,
			Optional:    true,
			Computed:    true,
			ElementType: types.StringType,
			Default:     setdefault.StaticValue(emptyStringSet()),
		}
	}

	resp.Schema = schema.Schema{
		Description: "Workers Builds のトリガー。リポジトリのどのブランチ・パスが変わったら、何を実行して Worker をデプロイするかを決める。\n\n" +
			"Builds の API はアカウントのトークンを受け付けないため、provider の `builds_api_token` にユーザーのトークン" +
			"（Workers Builds Configuration : Edit。API 上の権限名は `Workers CI Write`）を渡す。" +
			"リポジトリの接続（GitHub App のインストール）とビルドトークンはダッシュボードで作り、その UUID を指定する。",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "トリガーの UUID。",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"account_id": schema.StringAttribute{
				Description:   "アカウントの ID。変更するとリソースを作り直す。",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"script_name": schema.StringAttribute{
				Description: "トリガーを付ける Worker の名前。変更するとリソースを作り直す。" +
					"Worker は先に存在している必要がある（`cloudflare_workers_script` で作るなら、その `script_name` を参照する）。",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"script_tag": schema.StringAttribute{
				Description:   "Worker の tag（Builds の API が Worker を指すのに使う不変の ID）。",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"repo_connection_uuid": schema.StringAttribute{
				Description:   "リポジトリの接続の UUID。変更するとリソースを作り直す。",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"build_token_uuid": schema.StringAttribute{
				Description: "ビルドがデプロイに使うトークン（ビルドトークン）の UUID。",
				Required:    true,
			},
			"trigger_name": schema.StringAttribute{
				Description:   "トリガーの表示名。省略すると Worker の tag にする（ダッシュボードで作ったトリガーと同じ）。",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"build_command": schema.StringAttribute{
				Description: "ビルドのコマンド（例: `npm run build`）。",
				Required:    true,
			},
			"deploy_command": schema.StringAttribute{
				Description: "デプロイのコマンド（例: `npx wrangler deploy`）。",
				Required:    true,
			},
			"root_directory": schema.StringAttribute{
				Description: "コマンドを実行するディレクトリ（リポジトリのルートからの相対パス）。",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(""),
			},
			"branch_includes": stringSet("ビルドするブランチのパターン（例: `[\"main\"]`）。"),
			"branch_excludes": stringSet("ビルドしないブランチのパターン。"),
			"path_includes":   stringSet("変わったらビルドするファイルのパターン（例: `[\"apps/web/*\"]`）。"),
			"path_excludes":   stringSet("変わってもビルドしないファイルのパターン。"),
			"build_caching_enabled": schema.BoolAttribute{
				Description:   "ビルドのキャッシュを使うか。省略すると Cloudflare 側の値のまま。",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"environment_variables": schema.MapAttribute{
				Description: "ビルド時の環境変数（Build variables）。実行時の Worker からは見えない。",
				Optional:    true,
				ElementType: types.StringType,
			},
			"secret_environment_variables": schema.MapAttribute{
				Description: "ビルド時の環境変数のうち、ログで伏せるもの（Build secrets）。" +
					"Cloudflare は値を返さないため、Terraform の外での値の変更は検出できない（追加・削除は検出する）。",
				Optional:    true,
				Sensitive:   true,
				ElementType: types.StringType,
			},
		},
	}
}

func (r *workersBuildTriggerResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg workersBuildTriggerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	plain, d := stringMap(ctx, cfg.EnvironmentVariables)
	resp.Diagnostics.Append(d...)
	secret, d := stringMap(ctx, cfg.SecretEnvironmentVariables)
	resp.Diagnostics.Append(d...)
	for name := range plain {
		if _, dup := secret[name]; dup {
			resp.Diagnostics.AddAttributeError(path.Root("secret_environment_variables"), "Duplicate environment variable",
				fmt.Sprintf("環境変数 %q が environment_variables と secret_environment_variables の両方にあります。", name))
		}
	}
}

func (r *workersBuildTriggerResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data",
			fmt.Sprintf("*client.Client を期待しましたが %T が渡されました。provider の実装バグです。", req.ProviderData))
		return
	}
	r.client = c
}

func setStrings(ctx context.Context, s types.Set) ([]string, diag.Diagnostics) {
	out := []string{}
	if s.IsNull() || s.IsUnknown() {
		return out, nil
	}
	diags := s.ElementsAs(ctx, &out, false)
	sort.Strings(out)
	return out, diags
}

// buildTriggerFromModel は Terraform のモデルを API のリクエストに変換する。
func buildTriggerFromModel(ctx context.Context, m workersBuildTriggerModel) (client.BuildTrigger, diag.Diagnostics) {
	var diags diag.Diagnostics
	t := client.BuildTrigger{
		BuildTokenUUID: m.BuildTokenUUID.ValueString(),
		BuildCommand:   m.BuildCommand.ValueString(),
		DeployCommand:  m.DeployCommand.ValueString(),
		RootDirectory:  m.RootDirectory.ValueString(),
	}
	if !m.TriggerName.IsUnknown() {
		t.TriggerName = m.TriggerName.ValueString()
	}
	if !m.BuildCachingEnabled.IsUnknown() && !m.BuildCachingEnabled.IsNull() {
		v := m.BuildCachingEnabled.ValueBool()
		t.BuildCachingEnabled = &v
	}

	var d diag.Diagnostics
	t.BranchIncludes, d = setStrings(ctx, m.BranchIncludes)
	diags.Append(d...)
	t.BranchExcludes, d = setStrings(ctx, m.BranchExcludes)
	diags.Append(d...)
	t.PathIncludes, d = setStrings(ctx, m.PathIncludes)
	diags.Append(d...)
	t.PathExcludes, d = setStrings(ctx, m.PathExcludes)
	diags.Append(d...)
	return t, diags
}

// applyBuildTrigger は API から取得したトリガーを既存のモデルに反映した新しいモデルを返す。
func applyBuildTrigger(ctx context.Context, prior workersBuildTriggerModel, t client.BuildTrigger) (workersBuildTriggerModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	toSet := func(v []string) types.Set {
		if v == nil {
			// null の配列を空の set にそろえる（既定値の空 set と差分を出さないため）
			v = []string{}
		}
		s, d := types.SetValueFrom(ctx, types.StringType, v)
		diags.Append(d...)
		return s
	}

	next := prior
	next.ID = types.StringValue(t.TriggerUUID)
	next.ScriptTag = types.StringValue(t.ExternalScriptID)
	if t.RepoConnection != nil {
		next.RepoConnectionUUID = types.StringValue(t.RepoConnection.RepoConnectionUUID)
	} else if t.RepoConnectionUUID != "" {
		next.RepoConnectionUUID = types.StringValue(t.RepoConnectionUUID)
	}
	next.BuildTokenUUID = types.StringValue(t.BuildTokenUUID)
	next.TriggerName = types.StringValue(t.TriggerName)
	next.BuildCommand = types.StringValue(t.BuildCommand)
	next.DeployCommand = types.StringValue(t.DeployCommand)
	next.RootDirectory = types.StringValue(t.RootDirectory)
	next.BranchIncludes = toSet(t.BranchIncludes)
	next.BranchExcludes = toSet(t.BranchExcludes)
	next.PathIncludes = toSet(t.PathIncludes)
	next.PathExcludes = toSet(t.PathExcludes)
	if t.BuildCachingEnabled != nil {
		next.BuildCachingEnabled = types.BoolValue(*t.BuildCachingEnabled)
	} else if next.BuildCachingEnabled.IsUnknown() {
		next.BuildCachingEnabled = types.BoolNull()
	}
	return next, diags
}

// applyBuildEnvironmentVariables は API の Build variables を反映する。secret の値は API が返さないため、
// 既存のモデルの値を引き継ぐ（引き継げない場合は空文字にして差分を出す）。
func applyBuildEnvironmentVariables(ctx context.Context, prior workersBuildTriggerModel, vars map[string]client.BuildEnvironmentVariable) (workersBuildTriggerModel, diag.Diagnostics) {
	var diags diag.Diagnostics

	priorSecret, d := stringMap(ctx, prior.SecretEnvironmentVariables)
	diags.Append(d...)

	plain, secret := map[string]string{}, map[string]string{}
	for k, v := range vars {
		if v.IsSecret {
			secret[k] = priorSecret[k]
		} else {
			plain[k] = v.Value
		}
	}

	next := prior
	next.EnvironmentVariables, d = mapOrNull(ctx, prior.EnvironmentVariables, plain)
	diags.Append(d...)
	next.SecretEnvironmentVariables, d = mapOrNull(ctx, prior.SecretEnvironmentVariables, secret)
	diags.Append(d...)
	return next, diags
}

// syncEnvironmentVariables は Build variables を plan に合わせる。変わったものだけを送り、消えたものは消す。
func (r *workersBuildTriggerResource) syncEnvironmentVariables(ctx context.Context, accountID, triggerUUID string, prior, plan workersBuildTriggerModel) diag.Diagnostics {
	var diags diag.Diagnostics

	desired := map[string]client.BuildEnvironmentVariable{}
	current := map[string]client.BuildEnvironmentVariable{}
	for _, src := range []struct {
		m      types.Map
		secret bool
		into   map[string]client.BuildEnvironmentVariable
	}{
		{plan.EnvironmentVariables, false, desired},
		{plan.SecretEnvironmentVariables, true, desired},
		{prior.EnvironmentVariables, false, current},
		{prior.SecretEnvironmentVariables, true, current},
	} {
		vals, d := stringMap(ctx, src.m)
		diags.Append(d...)
		for k, v := range vals {
			src.into[k] = client.BuildEnvironmentVariable{Value: v, IsSecret: src.secret}
		}
	}
	if diags.HasError() {
		return diags
	}

	changed := map[string]client.BuildEnvironmentVariable{}
	for k, v := range desired {
		if cur, ok := current[k]; !ok || cur != v {
			changed[k] = v
		}
	}
	if len(changed) > 0 {
		if err := r.client.SetBuildEnvironmentVariables(ctx, accountID, triggerUUID, changed); err != nil {
			diags.AddError("Failed to set build environment variables",
				fmt.Sprintf("トリガー %s の Build variables を更新できませんでした: %s", triggerUUID, err))
			return diags
		}
	}

	removed := make([]string, 0)
	for k := range current {
		if _, ok := desired[k]; !ok {
			removed = append(removed, k)
		}
	}
	sort.Strings(removed)
	for _, k := range removed {
		if err := r.client.DeleteBuildEnvironmentVariable(ctx, accountID, triggerUUID, k); err != nil && !client.IsNotFound(err) {
			diags.AddError("Failed to delete build environment variable",
				fmt.Sprintf("トリガー %s の Build variable %q を削除できませんでした: %s", triggerUUID, k, err))
			return diags
		}
	}
	return diags
}

// refresh はトリガーと Build variables を取得してモデルに反映する。
func (r *workersBuildTriggerResource) refresh(ctx context.Context, m workersBuildTriggerModel) (workersBuildTriggerModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	accountID, tag, uuid := m.AccountID.ValueString(), m.ScriptTag.ValueString(), m.ID.ValueString()

	t, err := r.client.GetBuildTrigger(ctx, accountID, tag, uuid)
	if err != nil {
		diags.AddError("Failed to read build trigger", fmt.Sprintf("トリガー %s を取得できませんでした: %s", uuid, err))
		return m, diags
	}
	vars, err := r.client.ListBuildEnvironmentVariables(ctx, accountID, uuid)
	if err != nil {
		diags.AddError("Failed to read build environment variables", fmt.Sprintf("トリガー %s の Build variables を取得できませんでした: %s", uuid, err))
		return m, diags
	}

	next, d := applyBuildTrigger(ctx, m, t)
	diags.Append(d...)
	next, d = applyBuildEnvironmentVariables(ctx, next, vars)
	diags.Append(d...)
	return next, diags
}

func (r *workersBuildTriggerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan workersBuildTriggerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	accountID, name := plan.AccountID.ValueString(), plan.ScriptName.ValueString()
	tag, err := r.client.GetWorkerScriptTag(ctx, accountID, name)
	if err != nil {
		resp.Diagnostics.AddError("Failed to find Workers script",
			fmt.Sprintf("アカウント %s の Workers スクリプト %q を見つけられませんでした。トリガーより先に Worker を作ってください: %s", accountID, name, err))
		return
	}

	body, diags := buildTriggerFromModel(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	body.ExternalScriptID = tag
	body.RepoConnectionUUID = plan.RepoConnectionUUID.ValueString()
	if body.TriggerName == "" {
		// 作成時は trigger_name が必須（無いと 12002: Invalid request body）。
		// ダッシュボードで作ったトリガーと同じく、Worker の tag を名前にする。
		body.TriggerName = tag
	}

	created, err := r.client.CreateBuildTrigger(ctx, accountID, body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create build trigger",
			fmt.Sprintf("Workers スクリプト %q のトリガーを作成できませんでした: %s", name, err))
		return
	}

	// 作成済みのトリガーを追跡できるよう、以降が失敗しても ID は state に残す
	tracked := plan
	tracked.ID = types.StringValue(created.TriggerUUID)
	tracked.ScriptTag = types.StringValue(tag)
	empty := workersBuildTriggerModel{EnvironmentVariables: types.MapNull(types.StringType), SecretEnvironmentVariables: types.MapNull(types.StringType)}
	resp.Diagnostics.Append(r.syncEnvironmentVariables(ctx, accountID, created.TriggerUUID, empty, plan)...)

	next, diags := r.refresh(ctx, tracked)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		tracked.TriggerName = types.StringValue(created.TriggerName)
		if tracked.BuildCachingEnabled.IsUnknown() {
			tracked.BuildCachingEnabled = types.BoolNull()
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, tracked)...)
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
}

func (r *workersBuildTriggerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state workersBuildTriggerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	accountID, uuid := state.AccountID.ValueString(), state.ID.ValueString()
	if state.ScriptTag.IsNull() || state.ScriptTag.IsUnknown() {
		// import 直後
		tag, err := r.client.GetWorkerScriptTag(ctx, accountID, state.ScriptName.ValueString())
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		if err != nil {
			resp.Diagnostics.AddError("Failed to find Workers script",
				fmt.Sprintf("Workers スクリプト %q を取得できませんでした: %s", state.ScriptName.ValueString(), err))
			return
		}
		state.ScriptTag = types.StringValue(tag)
	}

	if _, err := r.client.GetBuildTrigger(ctx, accountID, state.ScriptTag.ValueString(), uuid); client.IsNotFound(err) {
		// Terraform の外で削除されたので state から外し、次の plan で再作成させる。
		resp.State.RemoveResource(ctx)
		return
	}

	next, diags := r.refresh(ctx, state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
}

func (r *workersBuildTriggerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state workersBuildTriggerModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	accountID, uuid := state.AccountID.ValueString(), state.ID.ValueString()
	body, diags := buildTriggerFromModel(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	if _, err := r.client.UpdateBuildTrigger(ctx, accountID, uuid, body); err != nil {
		resp.Diagnostics.AddError("Failed to update build trigger", fmt.Sprintf("トリガー %s を更新できませんでした: %s", uuid, err))
		return
	}
	resp.Diagnostics.Append(r.syncEnvironmentVariables(ctx, accountID, uuid, state, plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	plan.ID, plan.ScriptTag = state.ID, state.ScriptTag
	next, diags := r.refresh(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
}

func (r *workersBuildTriggerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state workersBuildTriggerModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	uuid := state.ID.ValueString()
	err := r.client.DeleteBuildTrigger(ctx, state.AccountID.ValueString(), uuid)
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to delete build trigger", fmt.Sprintf("トリガー %s を削除できませんでした: %s", uuid, err))
	}
}

// parseBuildTriggerImportID は `<account_id>/<script_name>/<trigger_uuid>` 形式の import ID を分解する。
func parseBuildTriggerImportID(id string) (accountID, scriptName, triggerUUID string, err error) {
	parts := strings.Split(id, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", "", "", fmt.Errorf("import ID は `<account_id>/<script_name>/<trigger_uuid>` 形式で指定してください（受け取った値: %q）", id)
	}
	return parts[0], parts[1], parts[2], nil
}

func (r *workersBuildTriggerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	accountID, scriptName, triggerUUID, err := parseBuildTriggerImportID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("account_id"), accountID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("script_name"), scriptName)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), triggerUUID)...)
}
