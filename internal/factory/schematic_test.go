package factory

import (
	"slices"
	"strings"
	"testing"
)

func TestAddExtensionsEmpty(t *testing.T) {
	s, err := ParseSchematic(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddExtensions("siderolabs/xen-guest-agent"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Extensions()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"siderolabs/xen-guest-agent"}; !slices.Equal(got, want) {
		t.Fatalf("extensions = %v, want %v", got, want)
	}
}

func TestAddExtensionsKeepsOtherFields(t *testing.T) {
	s, err := ParseSchematic([]byte(`
customization:
  extraKernelArgs:
    - console=ttyS0
  systemExtensions:
    officialExtensions:
      - siderolabs/iscsi-tools
      - siderolabs/xen-guest-agent
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddExtensions("siderolabs/xen-guest-agent", "siderolabs/util-linux-tools"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Extensions()
	want := []string{"siderolabs/iscsi-tools", "siderolabs/xen-guest-agent", "siderolabs/util-linux-tools"}
	if !slices.Equal(got, want) {
		t.Fatalf("extensions = %v, want %v", got, want)
	}
	out, err := s.YAML()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "console=ttyS0") {
		t.Fatalf("extraKernelArgs lost:\n%s", out)
	}
	if !s.Has("extraKernelArgs") || s.Has("meta") {
		t.Fatal("Has reports the wrong customization keys")
	}
}

func TestExtensionsRejectsBadShape(t *testing.T) {
	for _, doc := range []string{
		"customization: foo",
		"customization:\n  systemExtensions: [a]",
		"customization:\n  systemExtensions:\n    officialExtensions: foo",
		"customization:\n  systemExtensions:\n    officialExtensions: [1]",
	} {
		s, err := ParseSchematic([]byte(doc))
		if err != nil {
			continue // rejected at parse time is fine too
		}
		if err := s.AddExtensions("x"); err == nil {
			t.Errorf("AddExtensions accepted %q", doc)
		}
	}
}
