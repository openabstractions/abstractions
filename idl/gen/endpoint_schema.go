package main

// endpointTypes lowers the canonical base-service closure through the ordinary
// record codec owners. The compiler carries its derived source so standalone
// binaries need no checkout and emitted packages need no reverse module edge.
func endpointTypes() *Definition {
	s, err := parse(endpointSchema)
	if err != nil {
		panic(err)
	}
	s = enumCarriers(s)
	s.Document = ""
	for i := range s.Structs {
		s.Structs[i].Document = false
		s.Structs[i].CodecDepthLimit = s.Encoding.DepthLimit
		if s.Structs[i].Name == "EndpointRefusalDetail" {
			s.Structs[i].CodecDepthLimit = endpointRefusalDepthLimit
		}
	}
	s.Structs = append(s.Structs, Struct{Name: "EndpointDescriptionResult", Fields: serviceFields([]Field{{ID: 1, Name: "value", Type: "EndpointDescription", Omit: "never"}}), UnknownFields: "refuse"})
	return s
}

// Dispatch metadata uses the original wire shape, before backend enum lowering.
func endpointDefinitionHashes(s *Definition) map[string]string {
	out := map[string]string{}
	for _, row := range definitionHashes(s) {
		out[row[0]] = row[1]
	}
	return out
}
