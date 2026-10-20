package main

// selectedProviderExtensions detaches a router host's inventory from its
// selected, validated declaration. No other provider's keys are considered.
func selectedProviderExtensions(declared []providerExtension) map[string]map[string]bool {
	var supported map[string]map[string]bool
	for _, extension := range declared {
		if supported == nil {
			supported = make(map[string]map[string]bool)
		}
		if supported[extension.Profile] == nil {
			supported[extension.Profile] = make(map[string]bool)
		}
		supported[extension.Profile][extension.Key] = true
	}
	return supported
}
