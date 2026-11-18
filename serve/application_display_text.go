package main

import (
	"log"
	"unicode/utf8"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
)

// Display limits of an application's own words. A longer title or
// description is a display field: it is shortened and logged, and the
// description or presence that carries it stays usable.
const (
	applicationTitleBytes       = 256
	applicationDescriptionBytes = 4096
)

// fitDisplayText shortens s to at most limit bytes at a character boundary.
func fitDisplayText(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

// fitApplicationV3Description shortens each command's title and description
// to their display limits, and names a command without a title by its name.
func fitApplicationV3Description(d *appmodel.Description) {
	for i := range d.Commands {
		c := &d.Commands[i]
		if c.Title == "" {
			c.Title = c.Name
			log.Printf("applications: command %q has no title; showing its name", c.Name)
		}
		var cut bool
		if c.Title, cut = fitDisplayText(c.Title, applicationTitleBytes); cut {
			log.Printf("applications: command %q title shortened to %d bytes", c.Name, applicationTitleBytes)
		}
		if c.Description, cut = fitDisplayText(c.Description, applicationDescriptionBytes); cut {
			log.Printf("applications: command %q description shortened to %d bytes", c.Name, applicationDescriptionBytes)
		}
	}
}

// fitApplicationPresence shortens each announced context's title to its
// display limit. The caller's slice is copied before any change.
func fitApplicationPresence(p wire.ApplicationPresence) wire.ApplicationPresence {
	copied := false
	for i, v := range p.Contexts {
		title, cut := fitDisplayText(v.Title, applicationTitleBytes)
		if !cut {
			continue
		}
		if !copied {
			p.Contexts, copied = append([]wire.ApplicationContext(nil), p.Contexts...), true
		}
		p.Contexts[i].Title = title
		log.Printf("applications: %s context %q title shortened to %d bytes", p.Application, v.Name, applicationTitleBytes)
	}
	return p
}
