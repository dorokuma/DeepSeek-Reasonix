package agent

import "testing"

// TestStripTransientUserBlocksRemovesMemoryCompilerExecution guards the
// contract that the Memory v5 compiler's <memory-compiler-execution> block is
// treated as a transient, controller-injected block and stripped before the
// persisted user message becomes display text, a preview, or a title. The block
// rides in the user turn (so it never affects the stable prompt prefix) but is
// model-internal planning, not user-facing content.
func TestStripTransientUserBlocksRemovesMemoryCompilerExecution(t *testing.T) {
	block := "<memory-compiler-execution>\n{\"type\":\"memory_v5_execution_contract\",\"source_event\":\"add a config loader\"}\n</memory-compiler-execution>"

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "leading block then user text",
			in:   block + "\nadd a config loader",
			want: "add a config loader",
		},
		{
			name: "language blocks before memory-compiler block",
			// Real composition order: withTurnPreferences wraps the compiled
			// contract, so the language blocks lead and the compiler block
			// follows. The loop must peel every leading transient block.
			in: "<reasoning-language>zh</reasoning-language>\n\n" +
				"<response-language>zh</response-language>\n\n" + block + "\ndo the thing",
			want: "do the thing",
		},
		{
			name: "block only (compiled contract replaced the whole turn)",
			in:   block,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripTransientUserBlocks(tc.in); got != tc.want {
				t.Fatalf("StripTransientUserBlocks(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestUserPreviewTextRemovesMemoryCompilerExecution checks the higher-level
// preview helper (used for session titles and listing previews) also drops the
// compiler block rather than surfacing raw JSON.
func TestUserPreviewTextRemovesMemoryCompilerExecution(t *testing.T) {
	in := "<memory-compiler-execution>\n{\"type\":\"memory_v5_execution_contract\"}\n</memory-compiler-execution>\nship the refactor"
	if got := UserPreviewText(in); got != "ship the refactor" {
		t.Fatalf("UserPreviewText = %q, want %q", got, "ship the refactor")
	}
}
