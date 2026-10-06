package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/a2-ito/terraform-provider-cloudflare/internal/client"
)

var (
	_ datasource.DataSource              = (*zoneDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*zoneDataSource)(nil)
)

type zoneDataSource struct {
	client *client.Client
}

type zoneModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Status      types.String `tfsdk:"status"`
	Paused      types.Bool   `tfsdk:"paused"`
	NameServers types.List   `tfsdk:"name_servers"`
	AccountID   types.String `tfsdk:"account_id"`
}

func NewZoneDataSource() datasource.DataSource {
	return &zoneDataSource{}
}

func (d *zoneDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_zone"
}

func (d *zoneDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "名前から Cloudflare の Zone を引く。",
		Attributes: map[string]schema.Attribute{
			"name": schema.StringAttribute{
				Description: "Zone 名（例: example.com）。",
				Required:    true,
			},
			"id": schema.StringAttribute{
				Description: "Zone の ID。",
				Computed:    true,
			},
			"status": schema.StringAttribute{
				Description: "Zone のステータス（active, pending など）。",
				Computed:    true,
			},
			"paused": schema.BoolAttribute{
				Description: "Cloudflare のサービスが一時停止されているか。",
				Computed:    true,
			},
			"name_servers": schema.ListAttribute{
				Description: "Cloudflare が割り当てたネームサーバ。",
				ElementType: types.StringType,
				Computed:    true,
			},
			"account_id": schema.StringAttribute{
				Description: "Zone が属するアカウントの ID。",
				Computed:    true,
			},
		},
	}
}

func (d *zoneDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data",
			fmt.Sprintf("*client.Client を期待しましたが %T が渡されました。provider の実装バグです。", req.ProviderData))
		return
	}
	d.client = c
}

// zoneFromAPI は API の Zone を Terraform のモデルに変換する。
func zoneFromAPI(ctx context.Context, z client.Zone) (zoneModel, diag.Diagnostics) {
	nameServers, diags := types.ListValueFrom(ctx, types.StringType, z.NameServers)
	return zoneModel{
		ID:          types.StringValue(z.ID),
		Name:        types.StringValue(z.Name),
		Status:      types.StringValue(z.Status),
		Paused:      types.BoolValue(z.Paused),
		NameServers: nameServers,
		AccountID:   types.StringValue(z.Account.ID),
	}, diags
}

func (d *zoneDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg zoneModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	name := cfg.Name.ValueString()
	zones, err := d.client.ListZonesByName(ctx, name)
	if err != nil {
		resp.Diagnostics.AddError("Failed to look up zone", fmt.Sprintf("Zone %q を検索できませんでした: %s", name, err))
		return
	}
	switch len(zones) {
	case 0:
		resp.Diagnostics.AddError("Zone not found",
			fmt.Sprintf("Zone %q が見つかりません。名前と API Token の権限（Zone:Read）を確認してください。", name))
		return
	case 1:
	default:
		resp.Diagnostics.AddError("Multiple zones found",
			fmt.Sprintf("Zone %q が %d 件見つかりました。1 件に絞れません。", name, len(zones)))
		return
	}

	model, diags := zoneFromAPI(ctx, zones[0])
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, model)...)
}
