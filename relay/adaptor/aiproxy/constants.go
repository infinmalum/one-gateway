package aiproxy

import "github.com/infinmalum/one-gateway/relay/adaptor/openai"

var ModelList = []string{""}

func init() {
	ModelList = openai.ModelList
}
