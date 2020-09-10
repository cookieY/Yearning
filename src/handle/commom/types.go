package commom

type ExecuteStr struct {
	WorkId  string `json:"work_id"`
	Perform string `json:"perform"`
	Page    int    `json:"page"`
	Flag    int    `json:"flag"`
}

type PageInfo struct {
	Page int
	Find Search
	Tp   string
}

type Search struct {
	Picker []string `json:"picker"`
	Valve  bool     `json:"valve"`
	Text   string   `json:"text"`
}
