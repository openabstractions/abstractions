package main

import facade "github.com/openabstractions/abstraction-facade/go"

// panelMachine selects the installed runtime in production. Isolated tests supply
// a verified fixture binding without changing installation state or trust policy.
var panelMachine = facade.Discover
