package main

import _ "embed"

// thirdPartyNotices is THIRD_PARTY_NOTICES.txt (written by tools/notices):
// the licenses of the third-party code in this binary, printed by
// `rodin3d-mcp licenses`. Embedding it keeps the notices with every copy of
// the binary, including one that `update` downloaded alone.
//
//go:embed THIRD_PARTY_NOTICES.txt
var thirdPartyNotices string
