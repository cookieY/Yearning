package tpl

type Tpl struct {
	Desc    string   `json:"desc"`
	Auditor []string `json:"auditor"`
	Type    int      `json:"type"`
}

type tplTypes struct {
	Steps    []Tpl  `json:"steps"`
	Source   string `json:"source"`
	ID       int    `json:"id"`
	Relevant int    `json:"relevant"`
}

type ReqTpl struct {
	Source string `json:"source"`
	Page   int    ` json:"page"`
}
