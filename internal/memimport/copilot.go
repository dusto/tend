package memimport

// Copilot imports GitHub Copilot's repository instructions
// (.github/copilot-instructions.md) as a single always-on steering memory.
type Copilot struct{}

// Name identifies the adapter and is recorded as provenance source.
func (Copilot) Name() string { return "copilot" }

// Scan returns the Copilot instructions steering item, or nil when the file is
// absent.
func (Copilot) Scan(root string) ([]Item, error) {
	return singleSteeringFile(root, ".github/copilot-instructions.md", "copilot")
}
