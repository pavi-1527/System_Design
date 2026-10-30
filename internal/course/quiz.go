package course

// Quiz is a module's quiz.json: a pool of questions from which each attempt draws.
type Quiz struct {
	Module    string     `json:"module"`
	Title     string     `json:"title"`
	Draw      int        `json:"draw"`
	Questions []Question `json:"questions"`
}

// Question types.
const (
	QSingle  = "single"
	QMulti   = "multi"
	QNumeric = "numeric"
)

// Question is one quiz item. Choice questions use Choices+Answer (0-based indices);
// numeric questions use Value+Tolerance (relative) and an optional Unit label.
type Question struct {
	ID          string   `json:"id"`
	Type        string   `json:"type"`
	Tags        []string `json:"tags"`
	Difficulty  int      `json:"difficulty,omitempty"`
	Prompt      string   `json:"prompt"`
	Choices     []string `json:"choices,omitempty"`
	Answer      []int    `json:"answer,omitempty"`
	Value       float64  `json:"value,omitempty"`
	Tolerance   float64  `json:"tolerance,omitempty"`
	Unit        string   `json:"unit,omitempty"`
	Explanation string   `json:"explanation"`
	Ref         string   `json:"ref"`

	// Module is filled in when loaded through Course.QuestionIndex.
	Module string `json:"-"`
}
