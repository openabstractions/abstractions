package main

import (
	"fmt"
	"strings"
)

func validateObserveNames(svc Service) error {
	spelling := []func(Method) string{func(m Method) string { return goName(m.Name) }, cppMethod, pyMethodName, func(m Method) string { return camelCase(m.Name) }, func(m Method) string { return rustIdent(m.Name) }}
	for lane, name := range spelling {
		helpers := map[string]bool{}
		for _, m := range svc.Methods {
			if m.Observe == "" {
				continue
			}
			helper := name(Method{Name: m.Name + "Pages"})
			if lane == 0 {
				helper = goName(m.Name) + "Pages"
			}
			if helpers[helper] {
				return fmt.Errorf("generated observe helper collides with %s", helper)
			}
			helpers[helper] = true
			for _, other := range svc.Methods {
				if name(other) == helper {
					return fmt.Errorf("generated observe helper collides with %s", helper)
				}
			}
		}
	}
	return nil
}

// Observation metadata maps existing fields; it adds no wire carrier.
func validateObserve(s *Definition, m Method) error {
	if m.Oneway || s.Struct(m.Result.Type) == nil {
		return fmt.Errorf("observe needs a record reply")
	}
	cursor, next, wait := observeFields(s, m)
	if cursor.Name == "" || cursor.Type != "string" || cursor.Omit != "never" {
		return fmt.Errorf("observe needs required string argument %q", m.Observe)
	}
	if next.Name == "" || next.Type != "string" || next.Omit != "never" {
		return fmt.Errorf("observe_next needs required string result field %q", m.ObserveNext)
	}
	if wait.Name == "" || wait.Type != "i64" || wait.Omit != "never" {
		return fmt.Errorf("observe needs required i64 wait_ms argument")
	}
	return nil
}

var observeRangeExceptions = []struct{ wire, method, min, max string }{
	// Published @1 states this bound in its normative doc only; a range
	// annotation adds bad_value decoding to a published codec (CONV-V1).
	{"abstraction.config/observer@1", "Observe", "0", "30000"},
}

func observeWaitRange(svc Service, m Method, wait Field) (check, error) {
	var declared *check
	for i := range wait.Checks {
		if c := &wait.Checks[i]; c.Kind == checkRange && c.Element == "" {
			declared = c
			break
		}
	}
	for _, row := range observeRangeExceptions {
		if svc.WireName == row.wire && m.Name == row.method {
			if declared != nil {
				return check{}, fmt.Errorf("stale observation range exception %s.%s: method now declares a range", row.wire, row.method)
			}
			return check{Kind: checkRange, Min: row.min, Max: row.max}, nil
		}
	}
	if declared == nil {
		return check{}, fmt.Errorf("%s.%s: observe wait_ms requires a declared range or an exact published exception", svc.WireName, m.Name)
	}
	return *declared, nil
}

// Generation checks the complete definition before selected() projects it.
// Historical --hash/--compat parsing keeps its existing wire-shape language.
func validateObserveMethodRanges(s *Definition) error {
	for _, svc := range s.Services {
		for _, m := range svc.Methods {
			if m.Observe != "" {
				_, _, wait := observeFields(s, m)
				if _, err := observeWaitRange(svc, m, wait); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// Every direct backend emission shares the same ordinary error boundary.
func checkedEmitter(emit func(*Definition) string) func(*Definition) (string, error) {
	return func(s *Definition) (string, error) {
		if err := validateObserveMethodRanges(s); err != nil {
			return "", err
		}
		return emit(s), nil
	}
}

func validateObserveRanges(s *Definition) error {
	if err := validateObserveMethodRanges(s); err != nil {
		return err
	}
	for _, row := range observeRangeExceptions {
		owner, _, _ := strings.Cut(row.wire, "/")
		owned := false
		for _, namespace := range s.Namespaces {
			owned = owned || namespace == owner
		}
		if !owned {
			continue
		}
		found := false
		for _, svc := range s.Services {
			for _, m := range svc.Methods {
				if svc.WireName == row.wire && m.Name == row.method && m.Observe != "" {
					found = true
					_, _, wait := observeFields(s, m)
					if _, err := observeWaitRange(svc, m, wait); err != nil {
						return err
					}
				}
			}
		}
		if !found {
			return fmt.Errorf("stale observation range exception %s.%s: owning definition no longer declares this observation", row.wire, row.method)
		}
	}
	return nil
}

func observeFields(s *Definition, m Method) (cursor, next, wait Field) {
	for _, f := range m.Args {
		if f.Name == m.Observe {
			cursor = f
		}
		if f.Name == "wait_ms" {
			wait = f
		}
	}
	st := s.Struct(m.Result.Type)
	if imported, ok := s.importedRecord(m.Result.Type); ok {
		st = imported.Def.Struct(imported.Name)
	}
	if st != nil {
		for _, f := range st.Fields {
			if f.Name == m.ObserveNext {
				next = f
			}
		}
	}
	return
}

func observeRangef(b *strings.Builder, svc Service, m Method, wait Field, lang, format string, args ...any) {
	c, err := observeWaitRange(svc, m, wait)
	if err != nil {
		panic(err)
	} // Generation validates the complete definition before emission.
	min, max := c.Min, c.Max
	if min == "" {
		min = "-9223372036854775808"
	}
	if max == "" {
		max = "9223372036854775807"
	}
	bounds := min + ".." + max
	if lang == "cpp" {
		if min == "-9223372036854775808" {
			min = cppInt64(min)
		}
		if max == "9223372036854775807" {
			max = cppInt64(max)
		}
	}
	invalid := "invalid_request"
	if strings.HasSuffix(svc.WireName, "@2") {
		for _, code := range svc.ErrorCodes {
			if code == "invalid" {
				invalid = code
			}
		}
	}
	if invalid == "invalid" {
		format = strings.ReplaceAll(format, "raise ValueError(", `raise ServiceError("invalid", `)
		format = strings.ReplaceAll(format, "throw new RangeError(", `throw new ServiceError("invalid", `)
		format = strings.ReplaceAll(format, "throw std::invalid_argument(", `throw ServiceError("invalid", `)
	}
	format = strings.NewReplacer("@min@", min, "@max@", max, "@range@", bounds, "@invalid@", invalid).Replace(format)
	fmt.Fprintf(b, format, args...)
}

func observeLocal(m Method, lang, name string) string {
	for {
		used := false
		for _, f := range m.Args {
			n := f.Ident(lang)
			if lang == "go" {
				n = goParam(f, goReservedLocals)
			}
			if lang == "cpp" {
				n = cppParam(f)
			}
			if n == name {
				used = true
			}
		}
		if !used {
			return name
		}
		name += "_"
	}
}

func goObserve(b *strings.Builder, s *Definition, svc Service, m Method) {
	if m.Observe == "" {
		return
	}
	cursor, next, wait := observeFields(s, m)
	yield, page, err := observeLocal(m, "go", "_observeYield"), observeLocal(m, "go", "_observePage"), observeLocal(m, "go", "_observeErr")
	var args []string
	for _, f := range m.Args {
		args = append(args, goParam(f, goReservedLocals))
	}
	result := goType(s, m.Result)
	fmt.Fprintf(b, "// %sPages yields one bounded observation per request. A later range resumes\n// at the last successful cursor after an error; no call is retried inside a request.\nfunc(c *%sClient)%sPages(%s)func(func(%s,error)bool){return func(%s func(%s,error)bool){\n", goName(m.Name), svc.Name, goName(m.Name), goArgs(s, m, true), result, yield, result)
	observeRangef(b, svc, m, wait, "go", "if %s<@min@||%s>@max@{var %s %s;%s(%s,&ServiceError{Code:\"@invalid@\",Message:\"observe wait_ms is @range@\"});return}\n", goParam(wait, goReservedLocals), goParam(wait, goReservedLocals), page, result, yield, page)
	fmt.Fprintf(b, "for{%s,%s:=c.%s(%s);if %s==nil{%s=%s.%s};if !%s(%s,%s)||%s!=nil{return}}}}\n", page, err, goName(m.Name), strings.Join(args, ","), err, goParam(cursor, goReservedLocals), page, exported(next.Ident("go")), yield, page, err, err)
}

func pyObserve(b *strings.Builder, s *Definition, svc Service, m Method) {
	if m.Observe == "" {
		return
	}
	cursor, next, wait := observeFields(s, m)
	client, page, iterator := observeLocal(m, "python", "_observe_client"), observeLocal(m, "python", "_observe_page"), observeLocal(m, "python", "_ObservePages")
	fmt.Fprintf(b, "\n    def %s(self", pyMethodName(Method{Name: m.Name + "Pages"}))
	var args []string
	for _, f := range m.Args {
		fmt.Fprintf(b, ", %s: %s", f.Ident("python"), pyServiceType(s, f))
		args = append(args, f.Ident("python"))
	}
	observeRangef(b, svc, m, wait, "python", "):\n        if type(%s) is not int or not @min@ <= %s <= @max@:\n            raise ValueError(\"observe wait_ms is @range@\")\n        %s = self\n        class %s:\n            def __iter__(self): return self\n            def __next__(self):\n                nonlocal %s\n                %s = %s.%s(%s)\n                %s = %s.%s\n                return %s\n        return %s()\n", wait.Ident("python"), wait.Ident("python"), client, iterator, cursor.Ident("python"), page, client, pyMethodName(m), strings.Join(args, ", "), cursor.Ident("python"), page, next.Ident("python"), page, iterator)
}

func jsObserve(b *strings.Builder, s *Definition, svc Service, m Method) {
	if m.Observe == "" {
		return
	}
	cursor, next, wait := observeFields(s, m)
	client, page, tail, closed := observeLocal(m, "javascript", "_observeClient"), observeLocal(m, "javascript", "_observePage"), observeLocal(m, "javascript", "_observeTail"), observeLocal(m, "javascript", "_observeClosed")
	pending := observeLocal(m, "javascript", "_observePending")
	observeRangef(b, svc, m, wait, "javascript", "\n  %s(%s) {\n    if (typeof %s!==\"bigint\" || %s < @min@n || %s > @max@n) throw new RangeError(\"observe wait_ms is @range@\");\n    const %s=this; let %s=Promise.resolve(), %s=false;\n    return { [Symbol.asyncIterator]() { return this; },\n      next() { const %s=%s.then(async()=>{ if(%s)return {done:true,value:undefined}; const %s=await %s.%s(%s); %s=%s[%q];return {done:false,value:%s}; }); %s=%s.catch(()=>{});return %s; },\n      async return() { %s=true;return {done:true,value:undefined}; } };\n  }\n", camelCase(m.Name+"Pages"), jsParams(m), wait.Ident("javascript"), wait.Ident("javascript"), wait.Ident("javascript"), client, tail, closed, pending, tail, closed, page, client, camelCase(m.Name), jsParams(m), cursor.Ident("javascript"), page, next.Ident("javascript"), page, tail, pending, pending, closed)
}

func rsObserve(b *strings.Builder, s *Definition, svc Service) {
	for _, m := range svc.Methods {
		if m.Observe == "" {
			continue
		}
		cursor, next, wait := observeFields(s, m)
		invalid, closed := observeLocal(m, "rust", "_observe_invalid"), observeLocal(m, "rust", "_observe_closed")
		result, page := observeLocal(m, "rust", "_observe_result"), observeLocal(m, "rust", "_observe_page")
		var args []string
		for _, f := range m.Args {
			arg := f.Ident("rust")
			copy := f.Type == "i64" || f.Type == "bool" || (f.EnumType != nil && !f.EnumList && f.EnumType.Ann["unknown"] == "refuse")
			if !copy {
				arg += ".clone()"
			}
			args = append(args, arg)
		}
		fmt.Fprintf(b, "\nimpl<T: FrameTransport> %sClient<T> {\n    pub fn %s(&self%s) -> impl ::std::iter::Iterator<Item=::std::result::Result<%s,CallError<T::Error>>> + '_ {\n", svc.Name, rustIdent(m.Name+"Pages"), rsServiceArgs(s, m), rsServiceResult(s, m))
		observeRangef(b, svc, m, wait, "rust", "        let mut %s=%s; let mut %s=!(@min@..=@max@).contains(&%s); let mut %s=false;\n        ::std::iter::from_fn(move || {\n            if %s {return None;}\n            if %s {%s=false;%s=true;return Some(Err(CallError::Dispatch(\"@invalid@\")));}\n            let %s=self.%s(%s);\n            if let Ok(ref %s)=%s {%s=%s.%s.clone();}\n            Some(%s)\n        })\n    }\n}\n", cursor.Ident("rust"), cursor.Ident("rust"), invalid, wait.Ident("rust"), closed, closed, invalid, invalid, closed, result, rustIdent(m.Name), strings.Join(args, ","), page, result, cursor.Ident("rust"), page, next.Ident("rust"), result)
	}
}

func cppObserve(b *strings.Builder, s *Definition, svc Service) {
	for _, m := range svc.Methods {
		if m.Observe == "" {
			continue
		}
		cursor, next, wait := observeFields(s, m)
		var args []string
		for _, f := range m.Args {
			args = append(args, "args->"+f.Ident("cpp"))
		}
		local := observeLocal(m, "cpp", "_observe_args")
		observeRangef(b, svc, m, wait, "cpp", "auto %s(%s){if(%s<@min@||%s>@max@)throw std::invalid_argument(\"observe wait_ms is @range@\");detail::%s %s;\n", cppMethod(Method{Name: m.Name + "Pages"}), cppArgs(s, m), cppParam(wait), cppParam(wait), argsName(svc, m), local)
		for _, f := range m.Args {
			fmt.Fprintf(b, "%s.%s=%s;\n", local, f.Ident("cpp"), cppParam(f))
		}
		fmt.Fprintf(b, "using PageType=%s;struct Range {struct End{};struct Iterator {%sClient* client;detail::%s* args;PageType* value;bool* closed;\nIterator& operator++(){if(!*closed){auto page=client->%s(%s);args->%s=page.%s;*value=page;}return *this;}\nconst PageType& operator*()const{return *value;}bool operator!=(End)const{return !*closed;}void close(){*closed=true;}\n};%sClient* client;detail::%s args;PageType value{};bool closed=false;Iterator begin(){Iterator it{client,&args,&value,&closed};++it;return it;}End end(){return {};}};return Range{this,%s};}\n", cppReturn(s, m), svc.Name, argsName(svc, m), cppMethod(m), strings.Join(args, ","), cursor.Ident("cpp"), next.Ident("cpp"), svc.Name, argsName(svc, m), local)
	}
}
