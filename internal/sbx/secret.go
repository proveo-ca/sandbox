package sbx

func SecretSetArgs(name string) []string {
	return []string{"secret", "set", "--force", name}
}

func SecretSet(name, value string) error {
	return sh.SecretSet(name, value)
}
