package api

// EmbeddingFormat describes the text request and response fields of a resolved
// protocol. It contains no model/vendor guesses and does not probe a gateway.
type EmbeddingFormat struct {
	API         EmbeddingAPI `json:"api"`
	InputField  string       `json:"input_field"`
	OutputField string       `json:"output_field"`
}

func (s EmbeddingsSettings) TextFormat() EmbeddingFormat {
	f := EmbeddingFormat{API: s.API}
	switch s.API {
	case EmbeddingOpenAI:
		f.InputField, f.OutputField = "input[]", "data[].embedding"
		if s.TextAsMessages {
			f.InputField = "messages[].content[].text"
		}
	case EmbeddingDashScope:
		f.InputField, f.OutputField = "input.contents[].text", "output.embeddings[].embedding"
	case EmbeddingArk:
		f.InputField, f.OutputField = "input[].text", "data.embedding"
	case EmbeddingGoogle:
		f.InputField, f.OutputField = "requests[].content.parts[].text", "embeddings[].values"
	}
	return f
}
