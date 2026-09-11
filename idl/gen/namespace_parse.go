package main

import "fmt"

func (p *parser) namespaceDef() error {
	p.i++
	lang, e := p.ident()
	if p.toks[p.i-1].text == "*" {
		lang = "*"
		e = nil
	}
	if e != nil {
		return e
	}
	name, e := p.ident()
	if e != nil {
		return e
	}
	if p.def.Namespaces == nil {
		p.def.Namespaces = map[string]string{}
	}
	if _, ok := p.def.Namespaces[lang]; ok {
		return fmt.Errorf("namespace %s declared twice", lang)
	}
	p.def.Namespaces[lang] = name
	return nil
}
