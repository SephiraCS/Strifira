package dto

type PaginationParams struct {
	Page    int `query:"page" validate:"min=1"`
	PerPage int `query:"per_page" validate:"min=1,max=100"`
}

const (
	DefaultPerPage = 50
	MaxPerPage     = 100
	// MaxPage keeps OFFSET (page*per_page) far from integer overflow.
	MaxPage = 1_000_000
)

// Defaults fills unset values and clamps the rest into the supported range
// (page >= 1, 1 <= per_page <= MaxPerPage). Callers such as the public share
// list and MCP bind raw user input, so this is the single guard against
// unbounded LIMIT/OFFSET values.
func (p *PaginationParams) Defaults() {
	p.Page = max(1, min(p.Page, MaxPage))
	if p.PerPage == 0 {
		p.PerPage = DefaultPerPage
	}
	p.PerPage = max(1, min(p.PerPage, MaxPerPage))
}

// InRange reports whether the raw (pre-Defaults) values are acceptable for
// endpoints that reject, rather than clamp, out-of-range pagination.
func (p PaginationParams) InRange() bool {
	return p.Page >= 0 && p.Page <= MaxPage && p.PerPage >= 0 && p.PerPage <= MaxPerPage
}

func (p *PaginationParams) Offset() int {
	return (p.Page - 1) * p.PerPage
}

type ListResponse[T any] struct {
	Data       []T  `json:"data"`
	TotalCount int  `json:"total_count"`
	Page       int  `json:"page"`
	PerPage    int  `json:"per_page"`
	HasMore    bool `json:"has_more"`
}

type ErrorDetail struct {
	Field   string `json:"field,omitempty"`
	Message string `json:"message"`
}

type ErrorBody struct {
	Code    string        `json:"code"`
	Message string        `json:"message"`
	Details []ErrorDetail `json:"details,omitempty"`
}

type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}
