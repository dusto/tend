// Package memory backs the memory.* tools: task-bound, agent-authored notes the
// daemon can search and retrieve. The backend is a pluggable Provider behind a
// stable wire contract, so it can be swapped (e.g. for a vector store) without
// changing the methods. The default reads markdown files with YAML frontmatter
// under a workspace's memory directory, parsing the frontmatter into an in-memory
// index so search and get query structured entries rather than grepping files.
//
// memory.write upserts an entry (keyed by id) as a markdown file and is not
// approval-gated. The service emits memory_written on a write and memory_searched
// on a search, both on the workspace event stream, so a supervisor can observe
// what an agent records and recalls.
package memory
