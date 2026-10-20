package desktop

import "sort"

func mergeQuotaProfileLink(a, b quotaAccountProfile) quotaAccountProfile {
	if a.ExclusionState == "mixed" || b.ExclusionState == "mixed" || a.Excluded != nil && b.Excluded != nil && *a.Excluded != *b.Excluded {
		a.Excluded = nil
		a.ExclusionState = "mixed"
	} else if a.Excluded == nil || b.Excluded == nil {
		a.Excluded = nil
	}
	if a.Collecting != nil && *a.Collecting || b.Collecting != nil && *b.Collecting {
		v := true
		a.Collecting = &v
	} else if a.Collecting == nil || b.Collecting == nil {
		a.Collecting = nil
	} else {
		v := false
		a.Collecting = &v
	}
	return a
}
func mergeQuotaProfileLinks(links []quotaAccountProfile) []quotaAccountProfile {
	result := []quotaAccountProfile{}
	at := map[string]int{}
	for _, link := range links {
		if i, ok := at[link.ID]; ok {
			result[i] = mergeQuotaProfileLink(result[i], link)
		} else {
			at[link.ID] = len(result)
			result = append(result, link)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
