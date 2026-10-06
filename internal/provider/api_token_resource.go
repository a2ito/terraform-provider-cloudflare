package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/a2ito/terraform-provider-cloudflare/internal/client"
)

var (
	_ resource.Resource                = (*apiTokenResource)(nil)
	_ resource.ResourceWithConfigure   = (*apiTokenResource)(nil)
	_ resource.ResourceWithImportState = (*apiTokenResource)(nil)
	_ resource.ResourceWithModifyPlan  = (*apiTokenResource)(nil)
)

type apiTokenResource struct {
	client *client.Client
}

type apiTokenModel struct {
	ID         types.String            `tfsdk:"id"`
	AccountID  types.String            `tfsdk:"account_id"`
	Name       types.String            `tfsdk:"name"`
	Policies   []apiTokenPolicyModel   `tfsdk:"policies"`
	Status     types.String            `tfsdk:"status"`
	NotBefore  types.String            `tfsdk:"not_before"`
	ExpiresOn  types.String            `tfsdk:"expires_on"`
	Condition  *apiTokenConditionModel `tfsdk:"condition"`
	Value      types.String            `tfsdk:"value"`
	IssuedOn   types.String            `tfsdk:"issued_on"`
	ModifiedOn types.String            `tfsdk:"modified_on"`
}

type apiTokenPolicyModel struct {
	Effect           types.String   `tfsdk:"effect"`
	PermissionGroups []types.String `tfsdk:"permission_groups"`
	Resources        types.String   `tfsdk:"resources"`
}

type apiTokenConditionModel struct {
	RequestIP *apiTokenRequestIPModel `tfsdk:"request_ip"`
}

type apiTokenRequestIPModel struct {
	In    types.List `tfsdk:"in"`
	NotIn types.List `tfsdk:"not_in"`
}

func NewAPITokenResource() resource.Resource {
	return &apiTokenResource{}
}

func (r *apiTokenResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_api_token"
}

func (r *apiTokenResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Cloudflare の API Token。account_id を指定するとアカウントのトークン、省略するとユーザーのトークンを作る。" +
			"トークンの値（value）は作成時にしか取得できないため state に保存される。state は暗号化して保管すること。",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "トークンの ID。",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"account_id": schema.StringAttribute{
				Description:   "アカウントのトークンを作る場合のアカウント ID。省略するとユーザーのトークンになる。変更するとリソースを作り直す。",
				Optional:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Description: "トークンの名前。",
				Required:    true,
			},
			"policies": schema.ListNestedAttribute{
				Description:   "トークンに付けるアクセスポリシー。順序は区別しない（Cloudflare は更新のたびに並べ替えて返すため）。",
				Required:      true,
				PlanModifiers: []planmodifier.List{ignorePolicyOrder{}},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"effect": schema.StringAttribute{
							Description: "`allow` または `deny`。",
							Required:    true,
						},
						"permission_groups": schema.SetAttribute{
							Description: "権限グループの ID。`cloudflare_api_token_permission_groups` の `ids` で名前から引ける。",
							ElementType: types.StringType,
							Required:    true,
						},
						"resources": schema.StringAttribute{
							Description: "対象リソースの JSON。`jsonencode()` で書く（例: `jsonencode({\"com.cloudflare.api.account.zone.<zone_id>\" = \"*\"})`）。",
							Required:    true,
						},
					},
				},
			},
			"status": schema.StringAttribute{
				Description:   "`active` または `disabled`。",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"not_before": schema.StringAttribute{
				Description: "この日時より前は使えない（RFC 3339）。",
				Optional:    true,
			},
			"expires_on": schema.StringAttribute{
				Description: "有効期限（RFC 3339）。",
				Optional:    true,
			},
			"condition": schema.SingleNestedAttribute{
				Description: "トークンを使える条件。",
				Optional:    true,
				Attributes: map[string]schema.Attribute{
					"request_ip": schema.SingleNestedAttribute{
						Description: "接続元 IP の制限。",
						Optional:    true,
						Attributes: map[string]schema.Attribute{
							"in": schema.ListAttribute{
								Description: "許可する CIDR。",
								ElementType: types.StringType,
								Optional:    true,
							},
							"not_in": schema.ListAttribute{
								Description: "拒否する CIDR。",
								ElementType: types.StringType,
								Optional:    true,
							},
						},
					},
				},
			},
			"value": schema.StringAttribute{
				Description:   "トークンの値（secret）。作成時にしか取得できず、import した場合は null になる。",
				Computed:      true,
				Sensitive:     true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"issued_on": schema.StringAttribute{
				Description:   "発行日時。",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"modified_on": schema.StringAttribute{
				Description: "最終更新日時。",
				Computed:    true,
			},
		},
	}
}

func (r *apiTokenResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func stringsFromList(ctx context.Context, l types.List) ([]string, diag.Diagnostics) {
	if l.IsNull() || l.IsUnknown() {
		return nil, nil
	}
	var out []string
	diags := l.ElementsAs(ctx, &out, false)
	return out, diags
}

// apiTokenToAPI は Terraform のモデルを API のリクエストに変換する。
func apiTokenToAPI(ctx context.Context, m apiTokenModel) (client.APIToken, diag.Diagnostics) {
	var diags diag.Diagnostics

	policies := make([]client.TokenPolicy, 0, len(m.Policies))
	for i, p := range m.Policies {
		res := p.Resources.ValueString()
		if !json.Valid([]byte(res)) {
			diags.AddAttributeError(path.Root("policies").AtListIndex(i).AtName("resources"), "Invalid resources JSON",
				fmt.Sprintf("resources は JSON で指定してください（jsonencode() を使うと確実です）。受け取った値: %s", res))
			continue
		}
		groups := make([]client.PermissionGroupRef, 0, len(p.PermissionGroups))
		for _, g := range p.PermissionGroups {
			groups = append(groups, client.PermissionGroupRef{ID: g.ValueString()})
		}
		policies = append(policies, client.TokenPolicy{
			Effect:           p.Effect.ValueString(),
			PermissionGroups: groups,
			Resources:        json.RawMessage(res),
		})
	}

	var cond *client.TokenCondition
	if m.Condition != nil && m.Condition.RequestIP != nil {
		in, d := stringsFromList(ctx, m.Condition.RequestIP.In)
		diags.Append(d...)
		notIn, d := stringsFromList(ctx, m.Condition.RequestIP.NotIn)
		diags.Append(d...)
		cond = &client.TokenCondition{RequestIP: &client.TokenRequestIP{In: in, NotIn: notIn}}
	}

	status := ""
	if !m.Status.IsUnknown() {
		status = m.Status.ValueString()
	}

	return client.APIToken{
		Name:      m.Name.ValueString(),
		Status:    status,
		NotBefore: m.NotBefore.ValueString(),
		ExpiresOn: m.ExpiresOn.ValueString(),
		Policies:  policies,
		Condition: cond,
	}, diags
}

// preserveJSON は API が返した JSON が設定値と意味的に同じなら設定値を、違えば API の値を返す。
// キーの順序や空白の違いで差分が出ないようにする。
func preserveJSON(prior types.String, fromAPI json.RawMessage) types.String {
	if !prior.IsNull() && !prior.IsUnknown() {
		var a, b any
		if json.Unmarshal([]byte(prior.ValueString()), &a) == nil && json.Unmarshal(fromAPI, &b) == nil && reflect.DeepEqual(a, b) {
			return prior
		}
	}
	return types.StringValue(string(fromAPI))
}

// preserveTime は API が返した日時が設定値と同じ時刻なら設定値を返す（表記揺れで差分を出さない）。
func preserveTime(prior types.String, fromAPI string) types.String {
	if fromAPI == "" {
		return types.StringNull()
	}
	if !prior.IsNull() && !prior.IsUnknown() {
		p, err1 := time.Parse(time.RFC3339, prior.ValueString())
		a, err2 := time.Parse(time.RFC3339, fromAPI)
		if err1 == nil && err2 == nil && p.Equal(a) {
			return prior
		}
	}
	return types.StringValue(fromAPI)
}

// preserveList は API の値で list を作る。API が空で設定値も空リストなら、null ではなく設定値を返す。
func preserveList(prior types.List, fromAPI []string) types.List {
	if len(fromAPI) == 0 {
		if !prior.IsNull() && !prior.IsUnknown() && len(prior.Elements()) == 0 {
			return prior
		}
		return types.ListNull(types.StringType)
	}
	elems := make([]attr.Value, 0, len(fromAPI))
	for _, v := range fromAPI {
		elems = append(elems, types.StringValue(v))
	}
	return types.ListValueMust(types.StringType, elems)
}

func isEmptyCondition(c *apiTokenConditionModel) bool {
	if c == nil || c.RequestIP == nil {
		return true
	}
	empty := func(l types.List) bool { return l.IsNull() || len(l.Elements()) == 0 }
	return empty(c.RequestIP.In) && empty(c.RequestIP.NotIn)
}

func conditionFromAPI(prior *apiTokenConditionModel, fromAPI *client.TokenCondition) *apiTokenConditionModel {
	if fromAPI == nil || fromAPI.RequestIP == nil || (len(fromAPI.RequestIP.In) == 0 && len(fromAPI.RequestIP.NotIn) == 0) {
		// API 上は条件なし。設定側も実質空なら、書き方（{} や [] など）をそのまま残す。
		if isEmptyCondition(prior) {
			return prior
		}
		return nil
	}

	priorIP := &apiTokenRequestIPModel{In: types.ListNull(types.StringType), NotIn: types.ListNull(types.StringType)}
	if prior != nil && prior.RequestIP != nil {
		priorIP = prior.RequestIP
	}
	return &apiTokenConditionModel{
		RequestIP: &apiTokenRequestIPModel{
			In:    preserveList(priorIP.In, fromAPI.RequestIP.In),
			NotIn: preserveList(priorIP.NotIn, fromAPI.RequestIP.NotIn),
		},
	}
}

// apiTokenFromAPI は API のレスポンスと既存のモデルから新しいモデルを作る。
// value は作成時にしか返らないため、引数で受け取った値をそのまま入れる。
func apiTokenFromAPI(prior apiTokenModel, accountID types.String, value types.String, tok client.APIToken) apiTokenModel {
	// prior と同じ順序に並べてから、添字で対応する prior の値（resources の書き方）を引き継ぐ。
	apiPolicies := orderPoliciesLike(prior.Policies, tok.Policies)
	policies := make([]apiTokenPolicyModel, 0, len(apiPolicies))
	for i, p := range apiPolicies {
		priorResources := types.StringNull()
		if i < len(prior.Policies) {
			priorResources = prior.Policies[i].Resources
		}
		groups := make([]types.String, 0, len(p.PermissionGroups))
		for _, g := range p.PermissionGroups {
			groups = append(groups, types.StringValue(g.ID))
		}
		policies = append(policies, apiTokenPolicyModel{
			Effect:           types.StringValue(p.Effect),
			PermissionGroups: groups,
			Resources:        preserveJSON(priorResources, p.Resources),
		})
	}

	return apiTokenModel{
		ID:         types.StringValue(tok.ID),
		AccountID:  accountID,
		Name:       types.StringValue(tok.Name),
		Policies:   policies,
		Status:     types.StringValue(tok.Status),
		NotBefore:  preserveTime(prior.NotBefore, tok.NotBefore),
		ExpiresOn:  preserveTime(prior.ExpiresOn, tok.ExpiresOn),
		Condition:  conditionFromAPI(prior.Condition, tok.Condition),
		Value:      value,
		IssuedOn:   types.StringValue(tok.IssuedOn),
		ModifiedOn: types.StringValue(tok.ModifiedOn),
	}
}

func describeToken(accountID, tokenID string) string {
	if accountID == "" {
		return fmt.Sprintf("ユーザーの API Token %s", tokenID)
	}
	return fmt.Sprintf("アカウント %s の API Token %s", accountID, tokenID)
}

// ModifyPlan は、modified_on 以外が state と同じなら変更なしとして扱う。
//
// 設定と state が少しでも違うと（ポリシーの順序だけの違いでも）、framework は属性ごとの
// plan modifier より前に Computed の modified_on を unknown にする。ignorePolicyOrder で
// policies を state の値に戻しても modified_on が unknown のまま残り、更新が計画されてしまう。
func (r *apiTokenResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}

	var modifiedOn types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("modified_on"), &modifiedOn)...)
	if resp.Diagnostics.HasError() {
		return
	}

	candidate := resp.Plan
	resp.Diagnostics.Append(candidate.SetAttribute(ctx, path.Root("modified_on"), modifiedOn)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if candidate.Raw.Equal(req.State.Raw) {
		resp.Plan = candidate
	}
}

func (r *apiTokenResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan apiTokenModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body, diags := apiTokenToAPI(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	accountID := plan.AccountID.ValueString()
	tok, err := r.client.CreateAPIToken(ctx, accountID, body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to create API token",
			fmt.Sprintf("%s を作成できませんでした: %s", describeToken(accountID, plan.Name.ValueString()), err))
		return
	}
	value := types.StringValue(tok.Value)

	// 作成 API は status を受け付けないため、active 以外を指定された場合は続けて更新する。
	if body.Status != "" && body.Status != tok.Status {
		updated, err := r.client.UpdateAPIToken(ctx, accountID, tok.ID, body)
		if err != nil {
			// トークン自体はできているので state に残し、次の apply で status を合わせ直せるようにする。
			resp.Diagnostics.Append(resp.State.Set(ctx, apiTokenFromAPI(plan, plan.AccountID, value, tok))...)
			resp.Diagnostics.AddError("Failed to set API token status",
				fmt.Sprintf("%s は作成しましたが、status を %q にできませんでした: %s", describeToken(accountID, tok.ID), body.Status, err))
			return
		}
		tok = updated
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, apiTokenFromAPI(plan, plan.AccountID, value, tok))...)
}

func (r *apiTokenResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state apiTokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	accountID, tokenID := state.AccountID.ValueString(), state.ID.ValueString()
	tok, err := r.client.GetAPIToken(ctx, accountID, tokenID)
	if client.IsNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read API token",
			fmt.Sprintf("%s を取得できませんでした: %s", describeToken(accountID, tokenID), err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, apiTokenFromAPI(state, state.AccountID, state.Value, tok))...)
}

func (r *apiTokenResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state apiTokenModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body, diags := apiTokenToAPI(ctx, plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	accountID, tokenID := state.AccountID.ValueString(), state.ID.ValueString()
	tok, err := r.client.UpdateAPIToken(ctx, accountID, tokenID, body)
	if err != nil {
		resp.Diagnostics.AddError("Failed to update API token",
			fmt.Sprintf("%s を更新できませんでした: %s", describeToken(accountID, tokenID), err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, apiTokenFromAPI(plan, plan.AccountID, state.Value, tok))...)
}

func (r *apiTokenResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state apiTokenModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	accountID, tokenID := state.AccountID.ValueString(), state.ID.ValueString()
	err := r.client.DeleteAPIToken(ctx, accountID, tokenID)
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to delete API token",
			fmt.Sprintf("%s を削除できませんでした: %s", describeToken(accountID, tokenID), err))
	}
}

// parseAPITokenImportID は `<token_id>` または `<account_id>/<token_id>` 形式の import ID を分解する。
func parseAPITokenImportID(id string) (accountID, tokenID string, err error) {
	parts := strings.Split(id, "/")
	switch {
	case len(parts) == 1 && parts[0] != "":
		return "", parts[0], nil
	case len(parts) == 2 && parts[0] != "" && parts[1] != "":
		return parts[0], parts[1], nil
	default:
		return "", "", fmt.Errorf("import ID はユーザーのトークンなら `<token_id>`、アカウントのトークンなら `<account_id>/<token_id>` 形式で指定してください（受け取った値: %q）", id)
	}
}

func (r *apiTokenResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	accountID, tokenID, err := parseAPITokenImportID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	if accountID != "" {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("account_id"), accountID)...)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), tokenID)...)
}
