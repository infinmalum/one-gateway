package render

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/infinmalum/one-gateway/common"
)

// streamWriter is the writer surface the SSE render helpers need: response
// writes plus stream flushing. relay/adaptor.ResponseWriter implements it.
type streamWriter interface {
	http.ResponseWriter
	Flush()
}

func StringData(w streamWriter, str string) {
	str = strings.TrimPrefix(str, "data: ")
	str = strings.TrimSuffix(str, "\r")
	_ = common.CustomEvent{Data: "data: " + str}.Render(w)
	w.Flush()
}

func ObjectData(w streamWriter, object interface{}) error {
	jsonData, err := json.Marshal(object)
	if err != nil {
		return fmt.Errorf("error marshalling object: %w", err)
	}
	StringData(w, string(jsonData))
	return nil
}

func Done(w streamWriter) {
	StringData(w, "[DONE]")
}
