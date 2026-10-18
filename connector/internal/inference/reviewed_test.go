package inference

import (
	"os"
	"strings"
	"testing"
)

func TestShippedReviewedPinsAreValidAndInstallable(t *testing.T) {
	rev, err := ReviewedPins()
	if err != nil {
		t.Fatal(err)
	}
	c, err := LoadCatalogWithPins(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for id, pin := range rev.Pins {
		found := false
		for _, e := range c.Entries {
			if e.ID == id {
				found = true
				if !e.Installable || e.Revision != pin.Revision {
					t.Errorf("%s is not installable from its reviewed pin: %s", id, e.InstallBlockedReason)
				}
			}
		}
		if !found {
			t.Errorf("reviewed pin for unknown model %s", id)
		}
	}
}

func TestReviewedPinMakesModelInstallableWithoutPinning(t *testing.T) {
	good := strings.Repeat("a", 40)
	sum := strings.Repeat("b", 64)
	old := reviewedPinsJSON
	defer func() { reviewedPinsJSON = old }()
	reviewedPinsJSON = []byte(`{"schemaVersion":1,"pins":{"qwen3-4b-q4":{"hfRepo":"mlx-community/Qwen3-4B-4bit","revision":"` + good + `","license":"apache-2.0","modelType":"qwen3","files":[
	 {"name":"config.json","size":10,"sha256":"` + sum + `"},{"name":"tokenizer.json","size":10,"sha256":"` + sum + `"},
	 {"name":"tokenizer_config.json","size":10,"sha256":"` + sum + `"},{"name":"model.safetensors","size":100,"sha256":"` + sum + `"}]}}}`)
	c, err := LoadCatalogWithPins(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got := ReviewedModels(c)
	if len(got) != 1 || got[0].ID != "qwen3-4b-q4" || got[0].Revision != good || got[0].TotalBytes != 130 {
		t.Fatalf("%+v", got)
	}
	// An owner's own pins.json still wins over the shipped one.
	dir := t.TempDir()
	other := strings.Repeat("c", 40)
	_ = os.WriteFile(PinsPath(dir), []byte(`{"schemaVersion":1,"pins":{"qwen3-4b-q4":{"hfRepo":"mlx-community/Qwen3-4B-4bit","revision":"`+other+`","license":"apache-2.0","modelType":"qwen3","files":[
	 {"name":"config.json","size":10,"sha256":"`+sum+`"},{"name":"tokenizer.json","size":10,"sha256":"`+sum+`"},
	 {"name":"tokenizer_config.json","size":10,"sha256":"`+sum+`"},{"name":"model.safetensors","size":100,"sha256":"`+sum+`"}]}}}`), 0o600)
	c, err = LoadCatalogWithPins(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := ReviewedModels(c); len(got) != 1 || got[0].Revision != other {
		t.Fatalf("%+v", got)
	}
}
