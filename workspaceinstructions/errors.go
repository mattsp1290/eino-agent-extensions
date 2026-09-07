package workspaceinstructions

import "fmt"

func configError(code string) error {
	return fmt.Errorf("workspace instructions configuration invalid: code=%s", code)
}

func mountError(code string) error {
	return fmt.Errorf("workspace instructions mount invalid: code=%s", code)
}

func workspaceError(code string) error {
	return fmt.Errorf("workspace instructions workspace invalid: code=%s", code)
}

func providerError(code string) error {
	return fmt.Errorf("workspace instructions provider failed: code=%s", code)
}
