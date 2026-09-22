package itemtypes

import (
	"strings"
	"testing"
	"testing/fstest"
)

// TestDefaultRegistry is the guard that matters most: it runs the real
// embedded catalogue through the real validator. Adding a manifest with a
// typo'd slot reference, an unrenderable cover, or a slot nothing displays
// fails here rather than at runtime.
func TestDefaultRegistry(t *testing.T) {
	want := []string{"cd", "document", "photo", "photo-two-sided", "stack"}
	if got := Default.IDs(); !equalStrings(got, want) {
		t.Fatalf("type ids = %v, want %v", got, want)
	}

	for _, typ := range Default.All() {
		// Every type carries voice memos - the recordings about an object are
		// as much a part of an archive as the object.
		if _, ok := typ.Slot("voice-memo"); !ok {
			t.Errorf("type %q has no voice-memo slot", typ.ID)
		}
		if len(typ.Capabilities()) == 0 {
			t.Errorf("type %q derived no capabilities", typ.ID)
		}
	}
}

// TestCDSlotsAreAllOptional pins the behaviour the type system exists for: a
// CD with only an MP3, or only a photograph of the disc, is a valid CD.
func TestCDSlotsAreAllOptional(t *testing.T) {
	cd, ok := Default.Get("cd")
	if !ok {
		t.Fatal("cd type missing")
	}

	for _, slot := range cd.Slots {
		if slot.Min != 0 {
			t.Errorf("slot %q requires %d file(s); every CD slot should be optional", slot.ID, slot.Min)
		}
	}

	// And the composed viewer reaches for the existing primitives rather than
	// asking for a bespoke one.
	var primitives []string
	for _, p := range cd.Presentation {
		primitives = append(primitives, string(p.Primitive))
	}
	want := []string{"flippable", "single-image", "paged", "audio"}
	if !equalStrings(primitives, want) {
		t.Errorf("cd primitives = %v, want %v", primitives, want)
	}
}

func TestCapabilitiesAreStableAndDeduplicated(t *testing.T) {
	cd, _ := Default.Get("cd")

	// The CD has two audio slots (tracks and voice memos) and three image
	// slots; capabilities must collapse those, and always in the same order,
	// because the value is stored and indexed.
	got := cd.Capabilities()
	want := []MediaFamily{FamilyImage, FamilyAudio, FamilyPDF}
	if len(got) != len(want) {
		t.Fatalf("capabilities = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("capabilities = %v, want %v", got, want)
		}
	}

	photo, _ := Default.Get("photo")
	if caps := photo.Capabilities(); len(caps) != 2 || caps[0] != FamilyImage || caps[1] != FamilyAudio {
		t.Errorf("photo capabilities = %v, want [image audio]", caps)
	}
}

// TestSlotAliasResolves covers the rule that makes renaming a slot safe:
// files stored under the old id still find their slot.
func TestSlotAliasResolves(t *testing.T) {
	reg := mustLoad(t, map[string]string{"t.yaml": `
id: postcard
version: 2
label: Postcard
slots:
  - id: front
    label: Front
    accepts: [image]
    max: 1
    renamedFrom: [recto, face]
cover: { prefer: [front] }
presentation:
  - { primitive: single-image, slots: [front] }
`})

	typ, _ := reg.Get("postcard")
	for _, name := range []string{"front", "recto", "face"} {
		slot, ok := typ.Slot(name)
		if !ok {
			t.Fatalf("role %q did not resolve", name)
		}
		if slot.ID != "front" {
			t.Errorf("role %q resolved to %q, want front", name, slot.ID)
		}
	}
	if _, ok := typ.Slot("verso"); ok {
		t.Error("undeclared role resolved")
	}
}

func TestValidationRejects(t *testing.T) {
	tests := []struct {
		name     string
		manifest string
		wantErr  string
	}{
		{
			name: "slot with no max",
			manifest: `
id: t
version: 1
label: T
slots:
  - { id: a, label: A, accepts: [image] }
cover: { prefer: [a] }
presentation:
  - { primitive: single-image, slots: [a] }
`,
			wantErr: "max must be at least 1",
		},
		{
			name: "cover names a slot that holds no image",
			manifest: `
id: t
version: 1
label: T
slots:
  - { id: a, label: A, accepts: [audio], max: 1 }
cover: { prefer: [a] }
presentation:
  - { primitive: audio, slots: [a] }
`,
			wantErr: "holds no image or pdf",
		},
		{
			name: "slot nothing renders",
			manifest: `
id: t
version: 1
label: T
slots:
  - { id: a, label: A, accepts: [image], max: 1 }
  - { id: b, label: B, accepts: [audio], max: 5 }
cover: { prefer: [a] }
presentation:
  - { primitive: single-image, slots: [a] }
`,
			wantErr: `slot "b" is never rendered`,
		},
		{
			name: "slot rendered twice",
			manifest: `
id: t
version: 1
label: T
slots:
  - { id: a, label: A, accepts: [image], max: 1 }
cover: { prefer: [a] }
presentation:
  - { primitive: single-image, slots: [a] }
  - { primitive: stack, slots: [a] }
`,
			wantErr: "is rendered twice",
		},
		{
			name: "flippable with one slot",
			manifest: `
id: t
version: 1
label: T
slots:
  - { id: a, label: A, accepts: [image], max: 1 }
cover: { prefer: [a] }
presentation:
  - { primitive: flippable, slots: [a] }
`,
			wantErr: "takes exactly two slots",
		},
		{
			name: "single-image over a multi-file slot",
			manifest: `
id: t
version: 1
label: T
slots:
  - { id: a, label: A, accepts: [image], max: 9 }
cover: { prefer: [a] }
presentation:
  - { primitive: single-image, slots: [a] }
`,
			wantErr: "needs a slot with max 1",
		},
		{
			name: "unknown media family",
			manifest: `
id: t
version: 1
label: T
slots:
  - { id: a, label: A, accepts: [hologram], max: 1 }
cover: { prefer: [a] }
presentation:
  - { primitive: single-image, slots: [a] }
`,
			wantErr: "unknown media family",
		},
		{
			name: "unknown key is not silently ignored",
			manifest: `
id: t
version: 1
label: T
slotz:
  - { id: a, label: A, accepts: [image], max: 1 }
`,
			wantErr: "field slotz not found",
		},
		{
			name: "presentation names an alias instead of the slot",
			manifest: `
id: t
version: 1
label: T
slots:
  - { id: front, label: Front, accepts: [image], max: 1, renamedFrom: [recto] }
cover: { prefer: [front] }
presentation:
  - { primitive: single-image, slots: [recto] }
`,
			wantErr: "not its alias",
		},
		{
			name: "enum without options",
			manifest: `
id: t
version: 1
label: T
slots:
  - { id: a, label: A, accepts: [image], max: 1 }
attributes:
  - { id: finish, label: Finish, type: enum }
cover: { prefer: [a] }
presentation:
  - { primitive: single-image, slots: [a] }
`,
			wantErr: "enum needs options",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(fstest.MapFS{
				"manifests/t.yaml": &fstest.MapFile{Data: []byte(tt.manifest)},
			}, "manifests")
			if err == nil {
				t.Fatal("expected an error, got none")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestFamilyForContentType(t *testing.T) {
	tests := []struct {
		contentType string
		want        MediaFamily
		wantErr     bool
	}{
		{contentType: "image/jpeg", want: FamilyImage},
		{contentType: "image/heic", want: FamilyImage},
		{contentType: "IMAGE/PNG", want: FamilyImage},
		{contentType: "audio/mpeg", want: FamilyAudio},
		{contentType: "video/quicktime", want: FamilyVideo},
		{contentType: "application/pdf", want: FamilyPDF},
		{contentType: "application/pdf; version=1.7", want: FamilyPDF},
		{contentType: "model/gltf-binary", want: FamilyModel3D},
		{contentType: "application/octet-stream", wantErr: true},
		{contentType: "", wantErr: true},
	}

	for _, tt := range tests {
		got, err := FamilyForContentType(tt.contentType)
		if tt.wantErr {
			if err == nil {
				t.Errorf("FamilyForContentType(%q) = %q, want an error", tt.contentType, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("FamilyForContentType(%q) errored: %v", tt.contentType, err)
			continue
		}
		if got != tt.want {
			t.Errorf("FamilyForContentType(%q) = %q, want %q", tt.contentType, got, tt.want)
		}
	}
}

func mustLoad(t *testing.T, files map[string]string) *Registry {
	t.Helper()

	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys["manifests/"+name] = &fstest.MapFile{Data: []byte(body)}
	}

	reg, err := Load(fsys, "manifests")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return reg
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
