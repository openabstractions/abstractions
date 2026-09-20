package main

import (
	"fmt"
	"strings"
)

// genInterfaceOnly reuses the data backend with both RPC forms disabled.
// It adds only the same language method signatures used by normal service output.
func genInterfaceOnly(s *Definition, lang string) string {
	data := *s
	data.NoIPC = false
	data.Services = nil
	data.Proto = nil
	var body string
	switch lang {
	case "go":
		body = genGo(&data)
	case "cpp":
		body = genCpp(&data)
	case "python":
		body = genPy(&data)
	case "javascript":
		body = genJS(&data)
	default:
		panic("unsupported interface backend: " + lang)
	}
	var api strings.Builder
	for _, svc := range s.Services {
		switch lang {
		case "go":
			fmt.Fprintf(&api, "\ntype %s interface{\n", svc.Name)
			for _, method := range svc.Methods {
				fmt.Fprintf(&api, "%s(%s)%s\n", goName(method.Name), goArgs(s, method, false), goReturn(s, method, false))
			}
			api.WriteString("}\n")
		case "cpp":
			fmt.Fprintf(&api, "\nstruct %s{virtual ~%s()=default;\n", svc.Name, svc.Name)
			for _, method := range svc.Methods {
				fmt.Fprintf(&api, "virtual %s %s(%s)=0;\n", cppReturn(s, method), cppMethod(method), cppArgs(s, method))
			}
			api.WriteString("};\n")
		case "python":
			if len(svc.Methods) > 0 {
				pyInterface(&api, s, svc)
			}
		case "javascript":
			jsInterface(&api, svc)
		}
	}
	if lang == "cpp" {
		const close = "\n}  // namespace rec\n"
		return strings.TrimSuffix(body, close) + api.String() + close
	}
	return body + api.String()
}
