package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
)

// TestSCIMFilterValue covers the single supported operator.
//
// 只实现 eq 是刻意的：IdP 在 provisioning 时用 `userName eq "..."` 判断
// "这个人是否已存在"，这是唯一必要算子。完整 SCIM 过滤语法（and/or/co/sw）
// 对这个用途没有价值，实现它只会扩大攻击面。
func TestSCIMFilterValue(t *testing.T) {
	value, ok := scimFilterValue(`userName eq "zhangsan"`, "username")
	require.True(t, ok)
	assert.Equal(t, "zhangsan", value)

	_, ok = scimFilterValue(`userName co "zhang"`, "username")
	assert.False(t, ok, "only eq is supported")

	_, ok = scimFilterValue("", "username")
	assert.False(t, ok)

	_, ok = scimFilterValue(`displayName eq "x"`, "username")
	assert.False(t, ok, "a different attribute does not match")
}

// TestSCIMPatchExtractors covers the supported PATCH operations.
func TestSCIMPatchExtractors(t *testing.T) {
	active, ok := types.SCIMPatchActive([]types.SCIMPatchOperation{
		{Op: "replace", Path: "active", Value: false},
	})
	require.True(t, ok)
	assert.False(t, active)

	_, ok = types.SCIMPatchActive([]types.SCIMPatchOperation{
		{Op: "add", Path: "active", Value: true},
	})
	assert.False(t, ok, "add is not the replace op")

	name := types.SCIMPatchDisplayName([]types.SCIMPatchOperation{
		{Op: "Replace", Path: "displayName", Value: "新名字"},
	})
	assert.Equal(t, "新名字", name)

	assert.True(t, types.HasSCIMMemberRemoval([]types.SCIMPatchOperation{
		{Op: "remove", Path: `members[value eq "u-1"]`},
	}))
	assert.False(t, types.HasSCIMMemberRemoval([]types.SCIMPatchOperation{
		{Op: "add", Path: "members", Value: []interface{}{}},
	}))
}

// TestSCIMPatchableValue covers the value shapes an IdP sends.
func TestSCIMPatchableValue(t *testing.T) {
	assert.Equal(t, []string{"u-1", "u-2"}, scimMemberIDsFromValue([]interface{}{
		map[string]interface{}{"value": "u-1"},
		map[string]interface{}{"value": "u-2"},
	}))
	assert.Equal(t, []string{"u-1"}, scimMemberIDsFromValue(map[string]interface{}{"value": "u-1"}))
	assert.Equal(t, []string{"u-1"}, scimMemberIDsFromValue("u-1"))
	assert.Nil(t, scimMemberIDsFromValue(nil))
	assert.Nil(t, scimMemberIDsFromValue(42))
}

// TestSCIMPagingClamps covers the RFC 7644 paging bounds.
func TestSCIMPagingClamps(t *testing.T) {
	start, count := normalizeSCIMPaging(0, 0)
	assert.Equal(t, 1, start, "startIndex is 1-based")
	assert.Equal(t, scimDefaultPageSize, count)

	_, count = normalizeSCIMPaging(1, scimMaxPageSize+100)
	assert.Equal(t, scimMaxPageSize, count)

	// Pagination slices correctly and reports the unpaged total.
	all := []interface{}{"a", "b", "c", "d", "e"}
	page := paginateSCIM(all, 2, 2)
	assert.EqualValues(t, 5, page.TotalResults)
	assert.EqualValues(t, 2, page.StartIndex)
	assert.Equal(t, []interface{}{"b", "c"}, page.Resources)

	beyond := paginateSCIM(all, 99, 10)
	assert.Empty(t, beyond.Resources)
	assert.EqualValues(t, 5, beyond.TotalResults)
}
