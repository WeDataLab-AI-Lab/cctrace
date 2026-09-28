package store

import "strings"

// projectIdentityRemoteAuthority answers whether an incoming identity can
// replace an existing remote identity. An empty source is the legacy envelope:
// non-local IDs from older clients remain accepted, while an explicit fallback
// or unknown source is never promoted by string shape alone.
func projectIdentityRemoteAuthority(metadata ProjectIdentityMetadata) bool {
	id := strings.ToLower(strings.TrimSpace(metadata.RepositoryID))
	if id == "" || strings.HasPrefix(id, "local:") {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(metadata.RepositoryIDSource)) {
	case "", RepositoryIDSourceResolved:
		return true
	default:
		return false
	}
}

// projectIdentitySubpathFlags returns the two update permissions needed by the
// SQL upsert. The first permits an authoritative empty root to clear a stale
// value; the second permits an older legacy client to fill an empty value but
// never overwrite a non-empty one. Explicit fallback/unknown metadata gets no
// subpath write permission, because it could split a previously merged remote
// identity just as surely as changing repository_id would.
func projectIdentitySubpathFlags(metadata ProjectIdentityMetadata) (clear, fill bool) {
	remote := projectIdentityRemoteAuthority(metadata)
	clear = remote && metadata.RepoSubpathPresent
	fill = remote || (strings.TrimSpace(metadata.RepositoryIDSource) == "" && metadata.RepoSubpath != "")
	return clear, fill
}

func sessionRecordIdentityFlags(record *SessionRecord) (remote, clear, fill bool) {
	if record == nil {
		return false, false, false
	}
	metadata := ProjectIdentityMetadata{
		RepositoryID:       record.RepositoryID,
		RepositoryIDSource: record.RepositoryIDSource,
		RepoSubpath:        record.RepoSubpath,
		RepoSubpathPresent: record.RepoSubpathPresent,
	}
	remote = projectIdentityRemoteAuthority(metadata)
	clear, fill = projectIdentitySubpathFlags(metadata)
	return remote, clear, fill
}
