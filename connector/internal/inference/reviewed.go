package inference

import (
	_ "embed"
	"sort"
)

// Reviewed pins ship inside the app: for each model a maintainer has reviewed, the exact Hugging Face
// commit and the size and SHA-256 of every file. Installing such a model needs no pin step from the
// user; the download is verified against these values. Each release, review the new commit of every
// model and update reviewed-pins.json (connector/scripts/review-model.sh does the pin and the merge).
// A pins.json the owner wrote by hand still overrides these.
//
//go:embed reviewed-pins.json
var reviewedPinsJSON []byte

// ReviewedPins returns the pins shipped with this build.
func ReviewedPins() (*PinsFile, error) { return ParsePins(reviewedPinsJSON) }

// ReviewedModel is one model that installs with a single click.
type ReviewedModel struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	Revision    string `json:"revision"`
	TotalBytes  int64  `json:"totalBytes"`
}

// ReviewedModels lists the models of c that are installable from a pin (shipped or the owner's).
func ReviewedModels(c CatalogFile) []ReviewedModel {
	out := []ReviewedModel{}
	for _, e := range c.Entries {
		if !e.Installable {
			continue
		}
		var total int64
		for _, f := range e.Files {
			total += f.Size
		}
		out = append(out, ReviewedModel{ID: e.ID, DisplayName: e.DisplayName, Revision: e.Revision, TotalBytes: total})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
