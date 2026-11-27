package smbshare

import "testing"

// Called directly (encoding/json short-circuits nil pointers to null), a nil
// receiver must return a harmless value rather than panic. A nil *Credential or *Mode embedded in a status payload must serialize to a
// harmless value rather than panic on the nil receiver.
func TestMarshalJSONNilReceivers(t *testing.T) {
	var c *Credential
	b, err := c.MarshalJSON()
	if err != nil {
		t.Fatalf("nil credential marshal: %v", err)
	}
	if string(b) != "null" {
		t.Errorf("nil credential = %s, want null", b)
	}

	var m *Mode
	b, err = m.MarshalJSON()
	if err != nil {
		t.Fatalf("nil mode marshal: %v", err)
	}
	if string(b) != `{"recovery":false}` {
		t.Errorf("nil mode = %s, want {\"recovery\":false}", b)
	}
}
