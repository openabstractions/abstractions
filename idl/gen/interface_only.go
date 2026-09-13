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
				fmt.Fprintf(&api, "%s(%s)%s\n", exported(method.Name), goArgs(s, method, false), goReturn(s, method, false))
			}
			api.WriteString("}\n")
		case "cpp":
			fmt.Fprintf(&api, "\nstruct %s{virtual ~%s()=default;\n", svc.Name, svc.Name)
			for _, method := range svc.Methods {
				fmt.Fprintf(&api, "virtual %s %s(%s)=0;\n", cppReturn(s, method), method.Name, cppArgs(s, method))
			}
			api.WriteString("};\n")
		case "python":
			fmt.Fprintf(&api, "\n\nclass %s:\n    __doc__ = %q\n", svc.Name, svc.Doc)
			for _, method := range svc.Methods {
				fmt.Fprintf(&api, "    def %s(self", method.Name)
				for _, field := range method.Args {
					fmt.Fprintf(&api, ", %s: %q", field.Ident("python"), pyServiceType(field))
				}
				fmt.Fprintf(&api, ") -> %q:\n        raise NotImplementedError\n", pyServiceType(method.Result))
			}
		case "javascript":
			fmt.Fprintf(&api, "\nexport class %s {\n", svc.Name)
			for _, method := range svc.Methods {
				var args []string
				for i := range method.Args {
					args = append(args, fmt.Sprintf("arg%d", i))
				}
				fmt.Fprintf(&api, "  async %s(%s) { throw new Error(\"not implemented\"); }\n", method.Name, strings.Join(args, ","))
			}
			api.WriteString("}\n")
		}
	}
	if lang == "cpp" {
		const close = "\n}  // namespace rec\n"
		return strings.TrimSuffix(body, close) + api.String() + close
	}
	return body + api.String()
}
