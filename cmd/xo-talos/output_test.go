package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

var testResults = []result{
	{TemplateUUID: "d1bd676f-565f-fde8-6b96-24516c968fe2", PoolUUID: "6e0c5f16-923f-2971-a07d-ad1486f67309", TalosVersion: "v1.14.5", Created: true},
	{TemplateUUID: "11111111-565f-fde8-6b96-24516c968fe2", PoolUUID: "22222222-923f-2971-a07d-ad1486f67309", TalosVersion: "v1.14.5"},
}

func TestWriteResults(t *testing.T) {
	var buf bytes.Buffer
	if err := writeResults(&buf, "text", testResults); err != nil {
		t.Fatal(err)
	}
	if want := testResults[0].TemplateUUID + "\n" + testResults[1].TemplateUUID + "\n"; buf.String() != want {
		t.Errorf("text = %q", buf.String())
	}

	buf.Reset()
	if err := writeResults(&buf, "json", testResults); err != nil {
		t.Fatal(err)
	}
	var decoded []result
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil || len(decoded) != 2 || !decoded[0].Created {
		t.Errorf("json = %s (%v)", buf.String(), err)
	}

	buf.Reset()
	if err := writeResults(&buf, "env", testResults[:1]); err != nil {
		t.Fatal(err)
	}
	want := "export XO_TEMPLATE_UUID='d1bd676f-565f-fde8-6b96-24516c968fe2'\n" +
		"export XO_POOL_UUID='6e0c5f16-923f-2971-a07d-ad1486f67309'\n" +
		"export TALOS_VERSION='v1.14.5'\n"
	if buf.String() != want {
		t.Errorf("env = %q", buf.String())
	}
	if err := writeResults(&buf, "env", testResults); err == nil {
		t.Error("env accepted several pools")
	}
	if err := writeResults(&buf, "yaml", testResults); err == nil {
		t.Error("accepted an unknown format")
	}
}
