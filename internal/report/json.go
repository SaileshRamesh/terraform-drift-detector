// Package report renders scan results.
package report

import (
	"encoding/json"
	"io"

	"github.com/saileshramesh/terraform-drift-detector/internal/model"
)

func WriteJSON(w io.Writer, r *model.Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
