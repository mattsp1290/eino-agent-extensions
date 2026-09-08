package rtkreducer

import "fmt"

func configError(code string) error {
	return fmt.Errorf("rtk reducer configuration invalid: code=%s", code)
}

func mountError(code string) error {
	return fmt.Errorf("rtk reducer mount invalid: code=%s", code)
}

func cleanupError() error {
	return fmt.Errorf("rtk reducer cleanup incomplete")
}
