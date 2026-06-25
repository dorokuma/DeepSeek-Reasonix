package builtin

import "reasonix/internal/tool"

// ConfineWriters returns the file-writing built-ins (write_file, edit_file,
// multi_edit, move_file, notebook_edit).
func ConfineWriters(roots []string) []tool.Tool {
	return []tool.Tool{
		writeFile{},
		editFile{},
		multiEdit{},
		moveFile{},
		notebookEdit{},
		deleteRange{},
		deleteSymbol{},
	}
}
