package governance

import (
	"encoding/json"
	"testing"
)

func TestSourceRevisionBindsAuthorityIndependentlyOfRecoveryGeneration(t *testing.T) {
	base := record("source", Core)
	decode := func(revision uint64) Record {
		raw, err := json.Marshal(base)
		if err != nil {
			t.Fatal(err)
		}
		var data map[string]any
		if err = json.Unmarshal(raw, &data); err != nil {
			t.Fatal(err)
		}
		data["source_revision"] = revision
		raw, err = json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		var result Record
		if err = json.Unmarshal(raw, &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	one, two := decode(1), decode(2)
	if RecordFingerprint(one) == RecordFingerprint(two) {
		t.Fatal("source revision change did not invalidate authority fingerprint")
	}
	if RecordFingerprint(base) != RecordFingerprint(decode(0)) {
		t.Fatal("unbound legacy record fingerprint changed")
	}
	one.Generation++
	raw, err := json.Marshal(one)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err = json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["source_revision"] != float64(1) {
		t.Fatal("recovery generation changed or lost the original source revision")
	}
}
