package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/a2-ito/terraform-provider-cloudflare/internal/client"
)

var (
	_ resource.Resource                = (*dnsRecordResource)(nil)
	_ resource.ResourceWithConfigure   = (*dnsRecordResource)(nil)
	_ resource.ResourceWithImportState = (*dnsRecordResource)(nil)
)

type dnsRecordResource struct {
	client *client.Client
}

type dnsRecordModel struct {
	ID      types.String `tfsdk:"id"`
	ZoneID  types.String `tfsdk:"zone_id"`
	Name    types.String `tfsdk:"name"`
	Type    types.String `tfsdk:"type"`
	Content types.String `tfsdk:"content"`
	TTL     types.Int64  `tfsdk:"ttl"`
	Proxied types.Bool   `tfsdk:"proxied"`
	Comment types.String `tfsdk:"comment"`
}

func NewDNSRecordResource() resource.Resource {
	return &dnsRecordResource{}
}

func (r *dnsRecordResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_record"
}

func (r *dnsRecordResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Cloudflare の DNS レコード。",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Description:   "DNS レコードの ID。",
				Computed:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"zone_id": schema.StringAttribute{
				Description:   "レコードを作成する Zone の ID。変更するとリソースを作り直す。",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"name": schema.StringAttribute{
				Description: "レコード名。`www` のような短縮形でも FQDN でもよい（API が FQDN に正規化した場合も設定値を保持する）。",
				Required:    true,
			},
			"type": schema.StringAttribute{
				Description:   "レコードタイプ（A, AAAA, CNAME, TXT など）。変更するとリソースを作り直す。",
				Required:      true,
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"content": schema.StringAttribute{
				Description: "レコードの値。",
				Required:    true,
			},
			"ttl": schema.Int64Attribute{
				Description: "TTL（秒）。1 は automatic。",
				Optional:    true,
				Computed:    true,
				Default:     int64default.StaticInt64(1),
			},
			"proxied": schema.BoolAttribute{
				Description: "Cloudflare のプロキシを有効にするか。",
				Optional:    true,
				Computed:    true,
				Default:     booldefault.StaticBool(false),
			},
			"comment": schema.StringAttribute{
				Description: "レコードのコメント。",
				Optional:    true,
			},
		},
	}
}

func (r *dnsRecordResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// toAPI は Terraform のモデルを API のリクエストに変換する。
func toAPI(m dnsRecordModel) client.DNSRecord {
	return client.DNSRecord{
		Name:    m.Name.ValueString(),
		Type:    m.Type.ValueString(),
		Content: m.Content.ValueString(),
		TTL:     m.TTL.ValueInt64(),
		Proxied: m.Proxied.ValueBool(),
		Comment: m.Comment.ValueString(),
	}
}

// resolveName は API が返した名前と設定上の名前が同じレコードを指すなら設定上の名前を返す。
// Cloudflare は `www` を `www.example.com` のように FQDN に正規化して返すため、差分が出ないようにする。
func resolveName(configured, fromAPI string) string {
	if configured != "" && (fromAPI == configured || strings.HasPrefix(fromAPI, configured+".")) {
		return configured
	}
	return fromAPI
}

// fromAPI は API のレスポンスと既存のモデルから新しいモデルを作る。
func fromAPI(prior dnsRecordModel, zoneID string, rec client.DNSRecord) dnsRecordModel {
	comment := types.StringNull()
	if rec.Comment != "" {
		comment = types.StringValue(rec.Comment)
	}
	return dnsRecordModel{
		ID:      types.StringValue(rec.ID),
		ZoneID:  types.StringValue(zoneID),
		Name:    types.StringValue(resolveName(prior.Name.ValueString(), rec.Name)),
		Type:    types.StringValue(rec.Type),
		Content: types.StringValue(rec.Content),
		TTL:     types.Int64Value(rec.TTL),
		Proxied: types.BoolValue(rec.Proxied),
		Comment: comment,
	}
}

func (r *dnsRecordResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan dnsRecordModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	zoneID := plan.ZoneID.ValueString()
	rec, err := r.client.CreateDNSRecord(ctx, zoneID, toAPI(plan))
	if err != nil {
		resp.Diagnostics.AddError("Failed to create DNS record",
			fmt.Sprintf("zone %s に %s レコード %q を作成できませんでした: %s", zoneID, plan.Type.ValueString(), plan.Name.ValueString(), err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPI(plan, zoneID, rec))...)
}

func (r *dnsRecordResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state dnsRecordModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	zoneID, recordID := state.ZoneID.ValueString(), state.ID.ValueString()
	rec, err := r.client.GetDNSRecord(ctx, zoneID, recordID)
	if client.IsNotFound(err) {
		// Terraform の外で削除されたので state から外し、次の plan で再作成させる。
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Failed to read DNS record",
			fmt.Sprintf("zone %s の DNS レコード %s を取得できませんでした: %s", zoneID, recordID, err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPI(state, zoneID, rec))...)
}

func (r *dnsRecordResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state dnsRecordModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	zoneID, recordID := state.ZoneID.ValueString(), state.ID.ValueString()
	rec, err := r.client.UpdateDNSRecord(ctx, zoneID, recordID, toAPI(plan))
	if err != nil {
		resp.Diagnostics.AddError("Failed to update DNS record",
			fmt.Sprintf("zone %s の DNS レコード %s を更新できませんでした: %s", zoneID, recordID, err))
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, fromAPI(plan, zoneID, rec))...)
}

func (r *dnsRecordResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state dnsRecordModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	zoneID, recordID := state.ZoneID.ValueString(), state.ID.ValueString()
	err := r.client.DeleteDNSRecord(ctx, zoneID, recordID)
	if err != nil && !client.IsNotFound(err) {
		resp.Diagnostics.AddError("Failed to delete DNS record",
			fmt.Sprintf("zone %s の DNS レコード %s を削除できませんでした: %s", zoneID, recordID, err))
	}
}

// parseImportID は `<zone_id>/<record_id>` 形式の import ID を分解する。
func parseImportID(id string) (zoneID, recordID string, err error) {
	parts := strings.Split(id, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("import ID は `<zone_id>/<record_id>` 形式で指定してください（受け取った値: %q）", id)
	}
	return parts[0], parts[1], nil
}

func (r *dnsRecordResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	zoneID, recordID, err := parseImportID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("zone_id"), zoneID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), recordID)...)
}
