package testdata

func RetryLoop() error {
	for i := 0; i < 3; i++ {
		if err := runOnce(); err == nil {
			return nil
		}
	}
	return nil
}

func runOnce() error {
	return nil
}
