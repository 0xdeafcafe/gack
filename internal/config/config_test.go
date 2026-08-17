package config

import (
	"slices"
	"testing"
)

func TestSidebarSortCycleAndNormalization(t *testing.T) {
	if got := SidebarSort("").Normalize(); got != SidebarSortManual {
		t.Fatalf("empty sort normalized to %q", got)
	}
	if got := SidebarSort("future-value").Normalize(); got != SidebarSortManual {
		t.Fatalf("unknown sort normalized to %q", got)
	}

	sort := SidebarSortManual
	want := []SidebarSort{SidebarSortAlphabetical, SidebarSortAttention, SidebarSortManual}
	for index := range want {
		sort = sort.Next()
		if sort != want[index] {
			t.Fatalf("cycle step %d = %q, want %q", index, sort, want[index])
		}
	}
}

func TestApplyOrderIsStableForUnknownAndDuplicateIDs(t *testing.T) {
	type item struct{ id, value string }
	items := []item{
		{id: "new-a", value: "first unknown"},
		{id: "known-b", value: "known b"},
		{id: "new-b", value: "second unknown"},
		{id: "known-a", value: "known a"},
	}
	got := ApplyOrder(items, func(item item) string { return item.id }, []string{"known-a", "known-b", "known-a"})
	want := []string{"known-a", "known-b", "new-a", "new-b"}
	ids := make([]string, len(got))
	for index := range got {
		ids[index] = got[index].id
	}
	if !slices.Equal(ids, want) {
		t.Fatalf("ApplyOrder IDs = %v, want %v", ids, want)
	}
}
