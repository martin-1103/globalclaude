package tools

type PathAnchor struct {
	Path   string `json:"path"`
	Score  int    `json:"score"`
	Family string `json:"family,omitempty"`
	Why    string `json:"why,omitempty"`
}
