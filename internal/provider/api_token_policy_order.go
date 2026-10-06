package provider

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"

	"github.com/a2ito/terraform-provider-cloudflare/internal/client"
)

// Cloudflare はトークンを PUT で更新するとポリシーを作り直し、以降は別の順序で返す。
// ポリシーの順序に意味は無いため、ここでは「effect・権限グループの集合・resources（JSON として）」が
// 同じものを同じポリシーとみなし、順序の違いを差分として扱わないようにする。

// policyKey はポリシーを順序に依存せず比較するためのキーを返す。
// resources が JSON として読めない場合は ok = false を返す。
func policyKey(effect string, groups []string, resources string) (key string, ok bool) {
	var v any
	if err := json.Unmarshal([]byte(resources), &v); err != nil {
		return "", false
	}
	// encoding/json は map のキーを並べ替えて出力するため、意味が同じ JSON は同じ文字列になる。
	canonical, err := json.Marshal(v)
	if err != nil {
		return "", false
	}
	sorted := append([]string(nil), groups...)
	sort.Strings(sorted)
	return effect + "\x00" + strings.Join(sorted, ",") + "\x00" + string(canonical), true
}

func modelPolicyKey(p apiTokenPolicyModel) (string, bool) {
	if p.Effect.IsUnknown() || p.Resources.IsUnknown() {
		return "", false
	}
	groups := make([]string, 0, len(p.PermissionGroups))
	for _, g := range p.PermissionGroups {
		if g.IsUnknown() {
			return "", false
		}
		groups = append(groups, g.ValueString())
	}
	return policyKey(p.Effect.ValueString(), groups, p.Resources.ValueString())
}

func apiPolicyKey(p client.TokenPolicy) (string, bool) {
	groups := make([]string, 0, len(p.PermissionGroups))
	for _, g := range p.PermissionGroups {
		groups = append(groups, g.ID)
	}
	return policyKey(p.Effect, groups, string(p.Resources))
}

// orderPoliciesLike は API が返したポリシーを prior の順序に並べ替える。
// prior に対応するものが無いポリシーは、API の順序のまま末尾に置く。
func orderPoliciesLike(prior []apiTokenPolicyModel, fromAPI []client.TokenPolicy) []client.TokenPolicy {
	used := make([]bool, len(fromAPI))
	ordered := make([]client.TokenPolicy, 0, len(fromAPI))

	for _, p := range prior {
		pk, ok := modelPolicyKey(p)
		if !ok {
			continue
		}
		for i, a := range fromAPI {
			if used[i] {
				continue
			}
			if ak, ok := apiPolicyKey(a); ok && ak == pk {
				used[i] = true
				ordered = append(ordered, a)
				break
			}
		}
	}
	for i, a := range fromAPI {
		if !used[i] {
			ordered = append(ordered, a)
		}
	}
	return ordered
}

// policiesEquivalent は a と b が順序を除いて同じポリシーの集まりかどうかを返す。
func policiesEquivalent(a, b []apiTokenPolicyModel) bool {
	if len(a) != len(b) {
		return false
	}
	count := map[string]int{}
	for _, p := range a {
		k, ok := modelPolicyKey(p)
		if !ok {
			return false
		}
		count[k]++
	}
	for _, p := range b {
		k, ok := modelPolicyKey(p)
		if !ok || count[k] == 0 {
			return false
		}
		count[k]--
	}
	return true
}

// ignorePolicyOrder は、設定と state のポリシーが順序違いなだけなら state の値を plan に使う。
type ignorePolicyOrder struct{}

var _ planmodifier.List = ignorePolicyOrder{}

func (ignorePolicyOrder) Description(context.Context) string {
	return "ポリシーの順序の違いを差分として扱わない。"
}

func (m ignorePolicyOrder) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (ignorePolicyOrder) PlanModifyList(ctx context.Context, req planmodifier.ListRequest, resp *planmodifier.ListResponse) {
	if req.StateValue.IsNull() || req.PlanValue.IsNull() || req.PlanValue.IsUnknown() {
		return
	}

	var planned, current []apiTokenPolicyModel
	if diags := req.PlanValue.ElementsAs(ctx, &planned, false); diags.HasError() {
		// 値が未確定の要素を含むなどで読めない場合は、通常どおり差分を出す。
		return
	}
	if diags := req.StateValue.ElementsAs(ctx, &current, false); diags.HasError() {
		return
	}
	if policiesEquivalent(planned, current) {
		resp.PlanValue = req.StateValue
	}
}
