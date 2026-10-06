package provider

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/a2ito/terraform-provider-cloudflare/internal/client"
)

var (
	_ datasource.DataSource              = (*permissionGroupsDataSource)(nil)
	_ datasource.DataSourceWithConfigure = (*permissionGroupsDataSource)(nil)
)

type permissionGroupsDataSource struct {
	client *client.Client
}

type permissionGroupsModel struct {
	AccountID        types.String           `tfsdk:"account_id"`
	PermissionGroups []permissionGroupModel `tfsdk:"permission_groups"`
	IDs              map[string]string      `tfsdk:"ids"`
}

type permissionGroupModel struct {
	ID     string   `tfsdk:"id"`
	Name   string   `tfsdk:"name"`
	Scopes []string `tfsdk:"scopes"`
}

func NewPermissionGroupsDataSource() datasource.DataSource {
	return &permissionGroupsDataSource{}
}

func (d *permissionGroupsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_api_token_permission_groups"
}

func (d *permissionGroupsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "API Token に付けられる権限グループの一覧。`ids` で名前から ID を引ける。",
		Attributes: map[string]schema.Attribute{
			"account_id": schema.StringAttribute{
				Description: "指定するとアカウントのトークン用、省略するとユーザーのトークン用の一覧を返す。",
				Optional:    true,
			},
			"permission_groups": schema.ListNestedAttribute{
				Description: "権限グループの一覧。",
				Computed:    true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": schema.StringAttribute{
							Description: "権限グループの ID。",
							Computed:    true,
						},
						"name": schema.StringAttribute{
							Description: "権限グループの名前（例: DNS Write）。",
							Computed:    true,
						},
						"scopes": schema.ListAttribute{
							Description: "適用できるリソースの種類。",
							ElementType: types.StringType,
							Computed:    true,
						},
					},
				},
			},
			"ids": schema.MapAttribute{
				Description: "名前から ID への map。同じ名前が複数ある権限グループは、取り違えを防ぐため含めない（permission_groups から scopes を見て選ぶ）。",
				ElementType: types.StringType,
				Computed:    true,
			},
		},
	}
}

func (d *permissionGroupsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

// idsByName は名前から ID への map と、名前が重複していて map から外した名前を返す。
func idsByName(groups []client.PermissionGroup) (ids map[string]string, duplicated []string) {
	count := map[string]int{}
	for _, g := range groups {
		count[g.Name]++
	}
	ids = map[string]string{}
	for _, g := range groups {
		if count[g.Name] == 1 {
			ids[g.Name] = g.ID
		}
	}
	for name, n := range count {
		if n > 1 {
			duplicated = append(duplicated, name)
		}
	}
	sort.Strings(duplicated)
	return ids, duplicated
}

func (d *permissionGroupsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg permissionGroupsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	accountID := cfg.AccountID.ValueString()
	groups, err := d.client.ListPermissionGroups(ctx, accountID)
	if err != nil {
		resp.Diagnostics.AddError("Failed to list permission groups",
			fmt.Sprintf("権限グループの一覧を取得できませんでした（API Token に API Tokens Read 以上の権限が必要です）: %s", err))
		return
	}

	ids, duplicated := idsByName(groups)
	if len(duplicated) > 0 {
		resp.Diagnostics.AddWarning("Duplicated permission group names",
			fmt.Sprintf("次の名前は複数の権限グループで使われているため ids に含めていません。permission_groups から scopes を見て ID を選んでください: %s",
				strings.Join(duplicated, ", ")))
	}

	models := make([]permissionGroupModel, 0, len(groups))
	for _, g := range groups {
		scopes := g.Scopes
		if scopes == nil {
			scopes = []string{}
		}
		models = append(models, permissionGroupModel{ID: g.ID, Name: g.Name, Scopes: scopes})
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, permissionGroupsModel{
		AccountID:        cfg.AccountID,
		PermissionGroups: models,
		IDs:              ids,
	})...)
}
