package tools

type Hit struct {
	Source     string   `json:"source"`
	File       string   `json:"file"`
	LineStart  int      `json:"line_start"`
	LineEnd    int      `json:"line_end"`
	Symbol     string   `json:"symbol,omitempty"`
	Snippet    string   `json:"snippet,omitempty"`
	Why        string   `json:"why,omitempty"`
	Family     string   `json:"family,omitempty"`
	Lane       string   `json:"lane,omitempty"`
	EvidenceType string `json:"evidence_type,omitempty"`
	SupportRole  string `json:"support_role,omitempty"`
	Score      int      `json:"score,omitempty"`
	FusionScore float64 `json:"fusion_score,omitempty"`
	Confidence string   `json:"confidence,omitempty"`
	Tags       []string `json:"tags,omitempty"`
}

type TraceStep struct {
	Name          string `json:"name"`
	QualifiedName string `json:"qualified_name,omitempty"`
	File          string `json:"file,omitempty"`
	LineStart     int    `json:"line_start,omitempty"`
	LineEnd       int    `json:"line_end,omitempty"`
	Hop           int    `json:"hop"`
}

type TraceResult struct {
	Symbol    string      `json:"symbol"`
	Direction string      `json:"direction"`
	Mode      string      `json:"mode"`
	Callers   []TraceStep `json:"callers,omitempty"`
	Callees   []TraceStep `json:"callees,omitempty"`
}
