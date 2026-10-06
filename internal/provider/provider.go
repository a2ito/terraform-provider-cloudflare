package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/a2ito/terraform-provider-cloudflare/internal/client"
)

const (
	envAPIToken = "CLOUDFLARE_API_TOKEN"
	envBaseURL  = "CLOUDFLARE_BASE_URL"
)

var _ provider.Provider = (*cloudflareProvider)(nil)

type cloudflareProvider struct {
	version string
}

type cloudflareProviderModel struct {
	APIToken types.String `tfsdk:"api_token"`
	BaseURL  types.String `tfsdk:"base_url"`
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &cloudflareProvider{version: version}
	}
}

func (p *cloudflareProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "cloudflare"
	resp.Version = p.version
}

func (p *cloudflareProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Cloudflare API v4 を操作する Provider。",
		Attributes: map[string]schema.Attribute{
			"api_token": schema.StringAttribute{
				Description: "Cloudflare の API Token。未指定の場合は環境変数 " + envAPIToken + " を使う。",
				Optional:    true,
				Sensitive:   true,
			},
			"base_url": schema.StringAttribute{
				Description: "API のベース URL（主にテスト用）。未指定の場合は環境変数 " + envBaseURL + "、それも無ければ " + client.DefaultBaseURL + " を使う。",
				Optional:    true,
			},
		},
	}
}

// valueOrEnv は設定値があればそれを、無ければ環境変数の値を返す。
func valueOrEnv(v types.String, env string) string {
	if !v.IsNull() && !v.IsUnknown() {
		return v.ValueString()
	}
	return os.Getenv(env)
}

func (p *cloudflareProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg cloudflareProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if cfg.APIToken.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("api_token"), "Unknown Cloudflare API token",
			"api_token が apply 時まで確定しない値になっています。静的な値か環境変数 "+envAPIToken+" で指定してください。")
		return
	}

	token := valueOrEnv(cfg.APIToken, envAPIToken)
	if token == "" {
		resp.Diagnostics.AddAttributeError(path.Root("api_token"), "Missing Cloudflare API token",
			"provider の api_token か環境変数 "+envAPIToken+" で API Token を指定してください。")
		return
	}

	opts := []client.Option{}
	if baseURL := valueOrEnv(cfg.BaseURL, envBaseURL); baseURL != "" {
		opts = append(opts, client.WithBaseURL(baseURL))
	}

	c := client.New(token, opts...)
	resp.ResourceData = c
	resp.DataSourceData = c
}

func (p *cloudflareProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewDNSRecordResource,
	}
}

func (p *cloudflareProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewZoneDataSource,
	}
}
