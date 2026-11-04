package types

// ChunkContentView records a built-in reader's exact presentation of a stored
// chunk. Offsets count Unicode runes in the original repository body, before
// page clipping or content pagination. A hash alone does not prove a view:
// consumers must verify every range against an actually observed full body.
type ChunkContentView struct {
	OriginSHA256    string           `json:"origin_sha256"`
	OriginRevision  int              `json:"origin_revision"`
	OriginRuneCount int              `json:"origin_rune_count"`
	Ranges          []ChunkRuneRange `json:"ranges"`
}

type ChunkRuneRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

const MaxChunkContentViewRanges = 64
