package font

type GenerateRequest struct {
	FontID string
	Words  string
	Min    bool
	Weight string
	Format string
}

type GenerateResponse struct {
	Code      int      `json:"code"`
	Status    string   `json:"status"`
	Message   string   `json:"message"`
	Location  []string `json:"location"`
	Name      string   `json:"name"`
	Weight    int      `json:"weight,omitempty"`
	BuildMode string   `json:"buildMode,omitempty"`
}

type FontListItem struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Weight   []int    `json:"weight"`
	Author   *string  `json:"author"`
	NameZH   string   `json:"name_zh,omitempty"`
	NameEN   string   `json:"name_en,omitempty"`
	Category string   `json:"category,omitempty"`
	Tags     []string `json:"tags"`
	Family   string   `json:"family,omitempty"`
	SID      int      `json:"sid"`
}

type ListRequest struct {
	Search string
	Cursor string
	Limit  int
}

type ListResult struct {
	Items      []FontListItem
	NextCursor string
}

type FontInfoDTO struct {
	Name        FontInfoName `json:"name"`
	Category    string       `json:"category,omitempty"`
	Weight      []int        `json:"weight"`
	Tag         []string     `json:"tag"`
	Family      string       `json:"family,omitempty"`
	Version     string       `json:"version,omitempty"`
	License     string       `json:"license,omitempty"`
	Source      string       `json:"source,omitempty"`
	Author      *string      `json:"author"`
	Description string       `json:"description,omitempty"`
	Format      string       `json:"format,omitempty"`
	SID         int          `json:"sid"`
}

type FontInfoName struct {
	Original string `json:"original"`
	ZH       string `json:"zh,omitempty"`
	EN       string `json:"en,omitempty"`
}
