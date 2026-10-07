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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/a2ito/terraform-provider-cloudflare/internal/client"
)

var (
	_ resource.Resource                   = (*workersScriptResource)(nil)
	_ resource.ResourceWithConfigure      = (*workersScriptResource)(nil)
	_ resource.ResourceWithImportState    = (*workersScriptResource)(nil)
	_ resource.ResourceWithValidateConfig = (*workersScriptResource)(nil)
)

const (
	defaultMainModule = "worker.js"

	// importNoContentSuffix を import ID の末尾に付けると、content を管理しないモードとして取り込む。
	importNoContentSuffix = "no-content"
	// privateImportedWithContent は「import 直後で、本体まで読む」ことを Read に伝える private state のキー。
	// import 直後の state は content が null で、どちらのモードか区別できないため。
	privateImportedWithContent = "imported_with_content"

	// placeholderContent は content を管理しないモードで Worker を新しく作るときに置くスクリプト。
	// 以後のコードは Workers Builds などが置く。
	placeholderContent = `export default { fetch() { return new Response("This Worker has not been deployed yet.", { status: 503 }) } }`
)

type workersScriptResource struct {
	client *client.Client
}

type workersScriptModel struct {
	ID                 types.String `tfsdk:"id"`
	AccountID          types.String `tfsdk:"account_id"`
	ScriptName         types.String `tfsdk:"script_name"`
	Content            types.String `tfsdk:"content"`
	MainModule         types.String `tfsdk:"main_module"`
	CompatibilityDate  types.String `tfsdk:"compatibility_date"`
	CompatibilityFlags types.Set    `tfsdk:"compatibility_flags"`
	PlainTextBindings  types.Map    `tfsdk:"plain_text_bindings"`
	SecretTextBindings types.Map    `tfsdk:"secret_text_bindings"`
}

func NewWorkersScriptResource() resource.Resource {
	return &workersScriptResource{}
}

func (r *workersScriptResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_workers_script"
}

func (r *workersScriptResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Cloudflare Workers のスクリプト（ES Modules 形式・単一ファイル）。作成・更新するとそのままデプロイされる。\n\n" +
			"`content` を省略すると、コードを管理しないモードになる。Workers Builds や wrangler がデプロイする Worker の Secret だけを管理するときに使う。" +
			"このモードでは Secret を 1 件ずつ反映し、コードと他のバインディング（D1・KV・assets など）には触らない。" +
			"Worker が無ければ作成時に仮のスクリプトを置き、以後のコードは他の仕組みに任せる。",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "スクリプト名と同じ値。",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"account_id": schema.StringAttribute{
				Description:   "スクリプトを作成するアカウントの ID。変更するとリソースを作り直す。",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"script_name": schema.StringAttribute{
				Description:   "スクリプト名。変更するとリソースを作り直す。",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"content": schema.StringAttribute{
				Description: "スクリプト本体（ES Modules 形式）。`file(\"worker.js\")` のようにファイルから読み込むとよい。" +
					"省略するとコードを管理しないモードになり、`plain_text_bindings`・`main_module`・`compatibility_date`・`compatibility_flags` は指定できない" +
					"（wrangler のデプロイで上書きされるため）。",
				Optional: true,
			},
			"main_module": schema.StringAttribute{
				Description: "メインモジュールのファイル名。既定値は `worker.js`。",
				Optional:    true,
				Computed:    true,
				Default:     stringdefault.StaticString(defaultMainModule),
			},
			"compatibility_date": schema.StringAttribute{
				Description: "互換性日付（例: `2026-01-01`）。省略すると Cloudflare 側の既定値（`content` を省略した場合はデプロイされているもの）になる。",
				Optional:    true,
				Computed:    true,
			},
			"compatibility_flags": schema.SetAttribute{
				Description: "互換性フラグ（例: `nodejs_compat`）。",
				Optional:    true,
				ElementType: types.StringType,
			},
			"plain_text_bindings": schema.MapAttribute{
				Description: "平文の環境変数。キーがバインディング名、値がその内容。",
				Optional:    true,
				ElementType: types.StringType,
			},
			"secret_text_bindings": schema.MapAttribute{
				Description: "シークレットの環境変数。キーがバインディング名、値がその内容。" +
					"Cloudflare は値を返さないため、Terraform の外での値の変更は検出できない（バインディングの追加・削除は検出する）。",
				Optional:    true,
				Sensitive:   true,
				ElementType: types.StringType,
			},
		},
	}
}

func (r *workersScriptResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg workersScriptModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || !cfg.Content.IsNull() {
		return
	}

	// content を管理しないモードでは、デプロイ（wrangler deploy）が上書きする設定を持たせない。
	// 持たせると、デプロイのたびに Terraform との間で値が行き来する。
	conflicts := map[string]bool{
		"plain_text_bindings": !cfg.PlainTextBindings.IsNull(),
		"main_module":         !cfg.MainModule.IsNull(),
		"compatibility_date":  !cfg.CompatibilityDate.IsNull(),
		"compatibility_flags": !cfg.CompatibilityFlags.IsNull(),
	}
	for _, name := range []string{"plain_text_bindings", "main_module", "compatibility_date", "compatibility_flags"} {
		if conflicts[name] {
			resp.Diagnostics.AddAttributeError(path.Root(name), "Attribute requires content",
				fmt.Sprintf("%s は content を指定したときだけ使えます。content を省略した場合、この値は Worker をデプロイする側（wrangler の設定など）で持ってください。", name))
		}
	}
}

func (r *workersScriptResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func stringMap(ctx context.Context, m types.Map) (map[string]string, diag.Diagnostics) {
	out := map[string]string{}
	if m.IsNull() || m.IsUnknown() {
		return out, nil
	}
	diags := m.ElementsAs(ctx, &out, false)
	return out, diags
}

// sortedBindings は名前順に並べたバインディングを返す（リクエストを決定的にするため）。
func sortedBindings(bindingType string, m map[string]string) []client.WorkerBinding {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]client.WorkerBinding, 0, len(names))
	for _, name := range names {
		out = append(out, client.WorkerBinding{Type: bindingType, Name: name, Text: m[name]})
	}
	return out
}

// workersScriptMetadata は Terraform のモデルをアップロード用の metadata に変換する。
func workersScriptMetadata(ctx context.Context, m workersScriptModel) (client.WorkerScriptMetadata, diag.Diagnostics) {
	var diags diag.Diagnostics

	plain, d := stringMap(ctx, m.PlainTextBindings)
	diags.Append(d...)
	secret, d := stringMap(ctx, m.SecretTextBindings)
	diags.Append(d...)

	for name := range plain {
		if _, dup := secret[name]; dup {
			diags.AddAttributeError(path.Root("secret_text_bindings"), "Duplicate binding name",
				fmt.Sprintf("バインディング名 %q が plain_text_bindings と secret_text_bindings の両方にあります。", name))
		}
	}

	var flags []string
	if !m.CompatibilityFlags.IsNull() && !m.CompatibilityFlags.IsUnknown() {
		diags.Append(m.CompatibilityFlags.ElementsAs(ctx, &flags, false)...)
		sort.Strings(flags)
	}

	date := ""
	if !m.CompatibilityDate.IsUnknown() {
		date = m.CompatibilityDate.ValueString()
	}

	return client.WorkerScriptMetadata{
		MainModule:         m.MainModule.ValueString(),
		CompatibilityDate:  date,
		CompatibilityFlags: flags,
		Bindings:           append(sortedBindings(client.WorkerBindingPlainText, plain), sortedBindings(client.WorkerBindingSecretText, secret)...),
	}, diags
}

// mapOrNull は空のマップを、元の値が null なら null のまま返す（`{}` と未指定の差分を出さないため）。
func mapOrNull(ctx context.Context, prior types.Map, m map[string]string) (types.Map, diag.Diagnostics) {
	if len(m) == 0 && prior.IsNull() {
		return types.MapNull(types.StringType), nil
	}
	return types.MapValueFrom(ctx, types.StringType, m)
}

// applySettings は API から取得した設定を既存のモデルに反映した新しいモデルを返す。
// secret_text の値は API が返さないため、既存のモデルの値を引き継ぐ（引き継げない場合は空文字にして差分を出す）。
func applySettings(ctx context.Context, prior workersScriptModel, s client.WorkerScriptSettings) (workersScriptModel, diag.Diagnostics) {
	var diags diag.Diagnostics

	priorSecret, d := stringMap(ctx, prior.SecretTextBindings)
	diags.Append(d...)

	plain, secret := map[string]string{}, map[string]string{}
	for _, b := range s.Bindings {
		switch b.Type {
		case client.WorkerBindingPlainText:
			plain[b.Name] = b.Text
		case client.WorkerBindingSecretText:
			secret[b.Name] = priorSecret[b.Name]
		}
	}

	next := prior
	next.CompatibilityDate = types.StringValue(s.CompatibilityDate)

	if len(s.CompatibilityFlags) == 0 && prior.CompatibilityFlags.IsNull() {
		next.CompatibilityFlags = types.SetNull(types.StringType)
	} else {
		next.CompatibilityFlags, d = types.SetValueFrom(ctx, types.StringType, s.CompatibilityFlags)
		diags.Append(d...)
	}

	next.PlainTextBindings, d = mapOrNull(ctx, prior.PlainTextBindings, plain)
	diags.Append(d...)
	next.SecretTextBindings, d = mapOrNull(ctx, prior.SecretTextBindings, secret)
	diags.Append(d...)
	return next, diags
}

// upload はスクリプトをアップロードし、Cloudflare 側で決まる compatibility_date を反映したモデルを返す。
func (r *workersScriptResource) upload(ctx context.Context, plan workersScriptModel) (workersScriptModel, diag.Diagnostics) {
	var diags diag.Diagnostics

	meta, d := workersScriptMetadata(ctx, plan)
	diags.Append(d...)
	if diags.HasError() {
		return plan, diags
	}

	accountID, name := plan.AccountID.ValueString(), plan.ScriptName.ValueString()
	script, err := r.client.UploadWorkerScript(ctx, accountID, name, meta, plan.Content.ValueString())
	if err != nil {
		diags.AddError("Failed to upload Workers script",
			fmt.Sprintf("アカウント %s に Workers スクリプト %q をアップロードできませんでした: %s", accountID, name, err))
		return plan, diags
	}

	next := plan
	next.ID = types.StringValue(script.ID)

	if plan.CompatibilityDate.IsUnknown() {
		settings, err := r.client.GetWorkerScriptSettings(ctx, accountID, name)
		if err != nil {
			diags.AddError("Failed to read Workers script settings",
				fmt.Sprintf("アップロードした Workers スクリプト %q の設定を取得できませんでした: %s", name, err))
			return next, diags
		}
		next.CompatibilityDate = types.StringValue(settings.CompatibilityDate)
	}
	return next, diags
}

func (r *workersScriptResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan workersScriptModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if plan.Content.IsNull() {
		next, diags := r.createWithoutContent(ctx, plan)
		resp.Diagnostics.Append(diags...)
		if !next.ID.IsUnknown() {
			resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
		}
		return
	}

	next, diags := r.upload(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if next.ID.IsUnknown() {
		return
	}
	// 設定の取得だけ失敗した場合も、作成済みのスクリプトを追跡できるよう state に保存する
	if next.CompatibilityDate.IsUnknown() {
		next.CompatibilityDate = types.StringNull()
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
}

func (r *workersScriptResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state workersScriptModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	accountID, name := state.AccountID.ValueString(), state.ScriptName.ValueString()
	settings, err := r.client.GetWorkerScriptSettings(ctx, accountID, name)
	if client.IsNotFound(err) {
		// Terraform の外で削除されたので state から外し、次の plan で再作成させる。
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Workers script",
			fmt.Sprintf("アカウント %s の Workers スクリプト %q の設定を取得できませんでした: %s", accountID, name, err))
		return
	}

	importedWithContent, d := req.Private.GetKey(ctx, privateImportedWithContent)
	resp.Diagnostics.Append(d...)
	if state.Content.IsNull() && importedWithContent == nil {
		next, diags := readWithoutContent(ctx, state, settings)
		resp.Diagnostics.Append(diags...)
		if !resp.Diagnostics.HasError() {
			resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
		}
		return
	}
	if importedWithContent != nil {
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, privateImportedWithContent, nil)...)
	}

	content, err := r.client.GetWorkerScriptContent(ctx, accountID, name)
	if err != nil {
		resp.Diagnostics.AddError("Failed to read Workers script",
			fmt.Sprintf("アカウント %s の Workers スクリプト %q の本体を取得できませんでした: %s", accountID, name, err))
		return
	}

	next, diags := applySettings(ctx, state, settings)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	next.ID = types.StringValue(name)
	next.MainModule = types.StringValue(content.MainModule)
	next.Content = types.StringValue(content.Content)

	resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
}

func (r *workersScriptResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state workersScriptModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if plan.Content.IsNull() {
		next, diags := r.syncSecrets(ctx, state, plan)
		resp.Diagnostics.Append(diags...)
		if !resp.Diagnostics.HasError() {
			resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
		}
		return
	}

	next, diags := r.upload(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, next)...)
}

func (r *workersScriptResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state workersScriptModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	accountID, name := state.AccountID.ValueString(), state.ScriptName.ValueString()
	err := r.client.DeleteWorkerScript(ctx, accountID, name)
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to delete Workers script",
			fmt.Sprintf("アカウント %s の Workers スクリプト %q を削除できませんでした: %s", accountID, name, err))
	}
}

// parseWorkersScriptImportID は `<account_id>/<script_name>[/no-content]` 形式の import ID を分解する。
func parseWorkersScriptImportID(id string) (accountID, scriptName string, withContent bool, err error) {
	parts := strings.Split(id, "/")
	valid := (len(parts) == 2 || (len(parts) == 3 && parts[2] == importNoContentSuffix)) && parts[0] != "" && parts[1] != ""
	if !valid {
		return "", "", false, fmt.Errorf("import ID は `<account_id>/<script_name>`、content を管理しない場合は `<account_id>/<script_name>/%s` 形式で指定してください（受け取った値: %q）", importNoContentSuffix, id)
	}
	return parts[0], parts[1], len(parts) == 2, nil
}

func (r *workersScriptResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	accountID, scriptName, withContent, err := parseWorkersScriptImportID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	if withContent {
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, privateImportedWithContent, []byte("true"))...)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("account_id"), accountID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("script_name"), scriptName)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), scriptName)...)
}

// readWithoutContent は content を管理しないモードの Read。Secret の有無と compatibility_date だけを反映し、
// デプロイする側が持つ値（本体・平文のバインディング・互換性フラグ）は state に入れない。
func readWithoutContent(ctx context.Context, state workersScriptModel, s client.WorkerScriptSettings) (workersScriptModel, diag.Diagnostics) {
	var diags diag.Diagnostics

	priorSecret, d := stringMap(ctx, state.SecretTextBindings)
	diags.Append(d...)
	secret := map[string]string{}
	for _, b := range s.Bindings {
		if b.Type == client.WorkerBindingSecretText {
			secret[b.Name] = priorSecret[b.Name]
		}
	}

	next := state
	next.ID = types.StringValue(state.ScriptName.ValueString())
	next.CompatibilityDate = types.StringValue(s.CompatibilityDate)
	if next.MainModule.IsNull() {
		// import 直後。このモードでは使わないが、既定値と揃えて差分を出さない。
		next.MainModule = types.StringValue(defaultMainModule)
	}
	next.SecretTextBindings, d = mapOrNull(ctx, state.SecretTextBindings, secret)
	diags.Append(d...)
	return next, diags
}

// createWithoutContent は content を管理しないモードの Create。Worker が無いときだけ仮のスクリプトを置く。
// 既にある Worker を黙って取り込まないのは、別の Terraform 設定や手作業の Secret を上書きしないため。
func (r *workersScriptResource) createWithoutContent(ctx context.Context, plan workersScriptModel) (workersScriptModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	accountID, name := plan.AccountID.ValueString(), plan.ScriptName.ValueString()

	_, err := r.client.GetWorkerScriptSettings(ctx, accountID, name)
	if err == nil {
		diags.AddError("Workers script already exists",
			fmt.Sprintf("アカウント %s には Workers スクリプト %q が既にあります。Terraform で管理するには import してください: terraform import <address> %s/%s/%s",
				accountID, name, accountID, name, importNoContentSuffix))
		return plan, diags
	}
	if !client.IsNotFound(err) {
		diags.AddError("Failed to read Workers script",
			fmt.Sprintf("アカウント %s の Workers スクリプト %q を確認できませんでした: %s", accountID, name, err))
		return plan, diags
	}

	secret, d := stringMap(ctx, plan.SecretTextBindings)
	diags.Append(d...)
	if diags.HasError() {
		return plan, diags
	}
	meta := client.WorkerScriptMetadata{
		MainModule: plan.MainModule.ValueString(),
		Bindings:   sortedBindings(client.WorkerBindingSecretText, secret),
	}
	script, err := r.client.UploadWorkerScript(ctx, accountID, name, meta, placeholderContent)
	if err != nil {
		diags.AddError("Failed to create Workers script",
			fmt.Sprintf("アカウント %s に Workers スクリプト %q を作成できませんでした: %s", accountID, name, err))
		return plan, diags
	}

	next := plan
	next.ID = types.StringValue(script.ID)
	settings, err := r.client.GetWorkerScriptSettings(ctx, accountID, name)
	if err != nil {
		// 作成済みのスクリプトを追跡できるよう、ID は state に残す
		diags.AddError("Failed to read Workers script settings",
			fmt.Sprintf("作成した Workers スクリプト %q の設定を取得できませんでした: %s", name, err))
		next.CompatibilityDate = types.StringNull()
		return next, diags
	}
	next.CompatibilityDate = types.StringValue(settings.CompatibilityDate)
	return next, diags
}

// syncSecrets は content を管理しないモードの Update。変わった Secret だけを 1 件ずつ反映する。
func (r *workersScriptResource) syncSecrets(ctx context.Context, state, plan workersScriptModel) (workersScriptModel, diag.Diagnostics) {
	var diags diag.Diagnostics
	accountID, name := plan.AccountID.ValueString(), plan.ScriptName.ValueString()

	prior, d := stringMap(ctx, state.SecretTextBindings)
	diags.Append(d...)
	desired, d := stringMap(ctx, plan.SecretTextBindings)
	diags.Append(d...)
	if diags.HasError() {
		return state, diags
	}

	for _, b := range sortedBindings(client.WorkerBindingSecretText, desired) {
		if v, ok := prior[b.Name]; ok && v == b.Text {
			continue
		}
		if err := r.client.PutWorkerSecret(ctx, accountID, name, b.Name, b.Text); err != nil {
			diags.AddError("Failed to update Workers secret",
				fmt.Sprintf("Workers スクリプト %q の Secret %q を更新できませんでした: %s", name, b.Name, err))
			return state, diags
		}
	}
	for _, b := range sortedBindings(client.WorkerBindingSecretText, prior) {
		if _, ok := desired[b.Name]; ok {
			continue
		}
		if err := r.client.DeleteWorkerSecret(ctx, accountID, name, b.Name); err != nil && !client.IsNotFound(err) {
			diags.AddError("Failed to delete Workers secret",
				fmt.Sprintf("Workers スクリプト %q の Secret %q を削除できませんでした: %s", name, b.Name, err))
			return state, diags
		}
	}

	next := plan
	next.ID = state.ID
	if next.CompatibilityDate.IsUnknown() {
		next.CompatibilityDate = state.CompatibilityDate
	}
	return next, diags
}
