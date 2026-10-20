package setup

import (
	"fmt"
	"slices"
	"strings"
)

// graphicsFamilies names the graphics family of each display adapter an
// engine package in the catalog is built for, by its PCI vendor and device
// id. A card missing here belongs to no family, so a package built for one
// family is never offered for it.
var graphicsFamilies = map[string]string{
	// Ryzen AI Max (Strix Halo and Gorgon Halo): AMD's display driver INF
	// U0202643 (32.0.31035.1003) names the Radeon 8040S, 8050S, 8060S and
	// 8065S Graphics all under this one device id, and the owner's machine
	// reports it.
	`PCI\VEN_1002&DEV_1586`: "gfx1151",
}

// machineGraphics lists this computer's display adapters by their hardware
// ids (machine_graphics_windows.go); tests replace it.
var machineGraphics = displayAdapters

// graphicsOf is the families of the given hardware ids, such as
// PCI\VEN_1002&DEV_1586&SUBSYS_38291043&REV_C1.
func graphicsOf(hardware []string) []string {
	var families []string
	for _, id := range hardware {
		parts := strings.Split(strings.ToUpper(strings.TrimSpace(id)), "&")
		if len(parts) < 2 {
			continue
		}
		if family, ok := graphicsFamilies[parts[0]+"&"+parts[1]]; ok && !slices.Contains(families, family) {
			families = append(families, family)
		}
	}
	return families
}

// graphicsBlockers names each selected application choice built for a
// graphics family this computer's display adapters lack. A plan built for
// the gfx1151 runtime component requires a display adapter in that family.
func (c Catalog) graphicsBlockers(selected map[AppID]bool, families []string) []string {
	var blockers []string
	for _, choice := range c.Choices {
		if choice.Application && choice.Graphics != "" && selected[choice.ID] && !slices.Contains(families, choice.Graphics) {
			blockers = append(blockers, fmt.Sprintf("%s needs %s graphics and is not offered on this computer.", choice.Name(), graphicsName(choice.Graphics)))
		}
	}
	return blockers
}

// graphicsNames are the graphics families as a person knows them; a raw
// family code never reaches page text.
var graphicsNames = map[string]string{"gfx1151": "AMD Strix Halo"}

// graphicsName is family's name for a person.
func graphicsName(family string) string {
	if name, ok := graphicsNames[family]; ok {
		return name
	}
	return "other"
}

// machineFamilies is this computer's graphics families; an unreadable card
// fits no graphics-specific package.
func machineFamilies() []string {
	hardware, err := machineGraphics()
	if err != nil {
		return nil
	}
	return graphicsOf(hardware)
}

// Offered is whether this computer's graphics card fits app's choice, so the
// Panel offers it.
func (c Catalog) Offered(app AppID) bool {
	return len(c.graphicsBlockers(map[AppID]bool{app: true}, machineFamilies())) == 0
}

// engineFits is whether engine's choice fits a computer with the given
// graphics families.
func (c Catalog) engineFits(engine AppID, families []string) bool {
	for _, choice := range c.Choices {
		if choice.ID == engine {
			return choice.Graphics == "" || slices.Contains(families, choice.Graphics)
		}
	}
	return false
}
