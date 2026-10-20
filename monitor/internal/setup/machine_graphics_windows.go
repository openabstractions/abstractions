//go:build windows

package setup

import "golang.org/x/sys/windows/registry"

// displayClass is Windows' Display adapters device class; each numbered
// subkey is one installed adapter driver and names its hardware id.
const displayClass = `SYSTEM\CurrentControlSet\Control\Class\{4d36e968-e325-11ce-bfc1-08002be10318}`

// displayAdapters reads each display adapter's MatchingDeviceId. It reads
// the registry only and opens no device.
func displayAdapters() ([]string, error) {
	class, err := registry.OpenKey(registry.LOCAL_MACHINE, displayClass, registry.ENUMERATE_SUB_KEYS)
	if err != nil {
		return nil, err
	}
	defer class.Close()
	names, err := class.ReadSubKeyNames(-1)
	if err != nil {
		return nil, err
	}
	var hardware []string
	for _, name := range names {
		adapter, err := registry.OpenKey(class, name, registry.QUERY_VALUE)
		if err != nil {
			continue // the Properties subkey and drivers this account cannot read
		}
		id, _, err := adapter.GetStringValue("MatchingDeviceId")
		adapter.Close()
		if err == nil && id != "" {
			hardware = append(hardware, id)
		}
	}
	return hardware, nil
}
