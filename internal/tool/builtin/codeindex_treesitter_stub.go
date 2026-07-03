//go:build !treesitter || !cgo

package builtin

func (c codeIndex) parseTreeSitter(path string) ([]codeSymbol, bool, error) {
	return nil, false, nil
}

func codeIndexTreeSitterEnabled() bool {
	return false
}
